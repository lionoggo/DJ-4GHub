ARG DOCKER_REGISTRY=docker.io

FROM ${DOCKER_REGISTRY}/library/golang:1.26-bookworm AS build

WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath -buildvcs=false -ldflags='-s -w' \
    -o /out/dj4ghub ./cmd/dj4ghub-macos

FROM ${DOCKER_REGISTRY}/library/debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates espeak-ng libasound2 alsa-utils adb opus-tools \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/dj4ghub /usr/local/bin/dj4ghub
COPY packaging/qnap/dj4ghub-uac-call-recorder /usr/local/bin/dj4ghub-uac-call-recorder
COPY packaging/qnap/dj4ghub-module-voice-route /usr/local/bin/dj4ghub-module-voice-route
COPY mavo/Resources/ModuleVoice /usr/local/share/dj4ghub/module-voice
RUN chmod 755 /usr/local/bin/dj4ghub-uac-call-recorder /usr/local/bin/dj4ghub-module-voice-route \
    && chmod 755 /usr/local/share/dj4ghub/module-voice/mavo-pcm-bridge.armv7

EXPOSE 7575
ENTRYPOINT ["/usr/local/bin/dj4ghub"]
CMD ["-listen", "0.0.0.0:7575", "-automation-config", "/data/automation.json"]
