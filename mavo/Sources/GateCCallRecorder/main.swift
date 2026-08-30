import CUACProbe
import Darwin
import Foundation

private let vendorID: UInt16 = 0x2ca3
private let productID: UInt16 = 0x4006
private let locationID: UInt32 = 34_734_080
private let framesPerChunk = 160 // 20 ms at 8 kHz

private func failure(_ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(1)
}

private func errorText(_ probe: OpaquePointer) -> String {
    guard let raw = mavo_uac_probe_last_error(probe) else { return "unknown UAC error" }
    return String(cString: raw)
}

guard CommandLine.arguments.count == 3,
      let duration = TimeInterval(CommandLine.arguments[2]),
      duration > 0 else {
    failure("usage: GateCCallRecorder /absolute/path/to/output.raw duration-seconds")
}

let outputURL = URL(fileURLWithPath: CommandLine.arguments[1])
FileManager.default.createFile(atPath: outputURL.path, contents: nil)
let output: FileHandle
do {
    output = try FileHandle(forWritingTo: outputURL)
} catch {
    failure("open recording output: \(error.localizedDescription)")
}
defer {
    try? output.close()
}

guard let probe = mavo_uac_probe_create() else {
    failure("allocate UAC recorder")
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

print("UAC_RECORDER_READY duration=\(duration)")
let deadline = Date().addingTimeInterval(duration)
var frames = [Int16](repeating: 0, count: framesPerChunk)
var writtenFrames = 0
while Date() < deadline {
    let count = frames.withUnsafeMutableBufferPointer { buffer in
        Int(mavo_uac_probe_read_downlink_pcm16(probe, buffer.baseAddress!, framesPerChunk))
    }
    if count > 0 {
        // macOS runs on little-endian architectures, matching the s16le input
        // used by ffmpeg when this raw recording is finalized as a WAV file.
        frames.withUnsafeBytes { raw in
            output.write(Data(bytes: raw.baseAddress!, count: count * MemoryLayout<Int16>.size))
        }
        writtenFrames += count
    } else if mavo_uac_probe_is_running(probe) == 0 {
        failure("UAC stopped while recording: \(errorText(probe))")
    } else {
        Thread.sleep(forTimeInterval: 0.005)
    }
}
try? output.synchronize()
print("RECORDER_FINISHED frames=\(writtenFrames) callbacksIn=\(mavo_uac_probe_input_callbacks(probe)) signalSamples=\(mavo_uac_probe_input_signal_samples(probe)) peak=\(mavo_uac_probe_input_peak_pcm16(probe))")
