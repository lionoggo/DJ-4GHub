import CUACProbe
import Darwin
import Foundation

private let vendorID: UInt16 = 0x2ca3
private let productID: UInt16 = 0x4006
// This is the physical USB location of the connected Baiwang module. The UAC
// layer verifies the matching VID/PID and exact USB registry identity again.
private let locationID: UInt32 = 34_734_080
private let sampleRate = 8_000.0
private let framesPerChunk = 160 // 20 ms at 8 kHz
private let preRollFrames = 4_000 // 500 ms of silence while the route settles

private func failure(_ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(1)
}

private func errorText(_ probe: OpaquePointer) -> String {
    guard let raw = mavo_uac_probe_last_error(probe) else { return "unknown UAC error" }
    return String(cString: raw)
}

private func samples(from path: String) throws -> [Int16] {
    let bytes = [UInt8](try Data(contentsOf: URL(fileURLWithPath: path)))
    guard !bytes.isEmpty, bytes.count.isMultiple(of: 2) else {
        throw NSError(domain: "GateCPromptPlayer", code: 1, userInfo: [NSLocalizedDescriptionKey: "input must be nonempty 16-bit little-endian PCM"])
    }
    var result: [Int16] = []
    result.reserveCapacity(bytes.count / 2)
    for index in stride(from: 0, to: bytes.count, by: 2) {
        let bits = UInt16(bytes[index]) | UInt16(bytes[index + 1]) << 8
        result.append(Int16(bitPattern: bits))
    }
    return result
}

guard CommandLine.arguments.count == 2 else {
    failure("usage: GateCPromptPlayer /absolute/path/to/8khz-mono-s16le.pcm")
}

let pcmPath = CommandLine.arguments[1]
let pcm: [Int16]
do {
    pcm = try samples(from: pcmPath)
} catch {
    failure("read prompt: \(error.localizedDescription)")
}

guard let probe = mavo_uac_probe_create() else {
    failure("allocate UAC probe")
}
defer {
    if mavo_uac_probe_try_destroy(probe) != MAVO_UAC_OK {
        mavo_uac_probe_close(probe)
        _ = mavo_uac_probe_try_destroy(probe)
    }
}

let openCode = mavo_uac_probe_open_for_usb(probe, vendorID, productID, locationID, nil)
guard openCode == MAVO_UAC_OK else {
    failure("open verified module UAC: code=\(openCode) error=\(errorText(probe))")
}
guard mavo_uac_probe_usb_binding_verified(probe) != 0 else {
    failure("the selected UAC endpoint was not verified against the connected module")
}
guard mavo_uac_probe_start_pcm_bridge(probe) == MAVO_UAC_OK else {
    failure("start module UAC PCM: \(errorText(probe))")
}

let inputName = mavo_uac_probe_input_name(probe).map { String(cString: $0) } ?? "unknown"
let outputName = mavo_uac_probe_output_name(probe).map { String(cString: $0) } ?? "unknown"
print("UAC_READY input=\(inputName) output=\(outputName) pcmFrames=\(pcm.count)")

// The QDC507 UAC route can discard the first frames immediately after it is
// opened. Prime it with silence so the caller receives the prompt from its
// first spoken syllable rather than from the middle of the first word.
let silence = [Int16](repeating: 0, count: framesPerChunk)
var primedFrames = 0
while primedFrames < preRollFrames {
    let count = min(framesPerChunk, preRollFrames - primedFrames)
    let accepted = silence.withUnsafeBufferPointer { buffer in
        Int(mavo_uac_probe_write_uplink_pcm16(probe, buffer.baseAddress!, count))
    }
    if accepted > 0 {
        primedFrames += accepted
        Thread.sleep(forTimeInterval: Double(accepted) / sampleRate)
    } else if mavo_uac_probe_is_running(probe) == 0 {
        failure("UAC stopped while priming: \(errorText(probe))")
    } else {
        Thread.sleep(forTimeInterval: 0.005)
    }
}

let begun = DispatchTime.now().uptimeNanoseconds
var offset = 0
while offset < pcm.count {
    let count = min(framesPerChunk, pcm.count - offset)
    let accepted = pcm.withUnsafeBufferPointer { buffer in
        Int(mavo_uac_probe_write_uplink_pcm16(probe, buffer.baseAddress!.advanced(by: offset), count))
    }
    if accepted > 0 {
        offset += accepted
        let target = Double(offset) / sampleRate
        let elapsed = Double(DispatchTime.now().uptimeNanoseconds - begun) / 1_000_000_000
        if target > elapsed {
            Thread.sleep(forTimeInterval: min(target - elapsed, 0.025))
        }
    } else {
        if mavo_uac_probe_is_running(probe) == 0 {
            failure("UAC stopped while sending: \(errorText(probe))")
        }
        Thread.sleep(forTimeInterval: 0.005)
    }
}

Thread.sleep(forTimeInterval: 0.45)
let inputCallbacks = mavo_uac_probe_input_callbacks(probe)
let outputCallbacks = mavo_uac_probe_output_callbacks(probe)
let inputFrames = mavo_uac_probe_input_frames(probe)
let outputFrames = mavo_uac_probe_output_frames(probe)
let inputSignalSamples = mavo_uac_probe_input_signal_samples(probe)
let inputPeakPCM16 = mavo_uac_probe_input_peak_pcm16(probe)
guard outputCallbacks > 0, outputFrames >= UInt64(pcm.count) else {
    failure("UAC output did not consume the prompt (callbacks=\(outputCallbacks), frames=\(outputFrames))")
}
print("PROMPT_SENT callbacksIn=\(inputCallbacks) callbacksOut=\(outputCallbacks) inputFrames=\(inputFrames) inputSignalSamples=\(inputSignalSamples) inputPeakPCM16=\(inputPeakPCM16) outputFrames=\(outputFrames)")
