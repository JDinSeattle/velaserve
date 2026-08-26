FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/simfleet ./cmd/simfleet \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/fanoutbench ./cmd/fanoutbench \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/zeroprobe ./cmd/zeroprobe \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/velaserve-gate ./cmd/velaserve-gate \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/oracle-replay ./cmd/oracle-replay \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/schema-check ./cmd/schema-check \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/condition-controller ./cmd/condition-controller \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/condition-driver ./cmd/condition-driver \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/clock-probe ./cmd/clock-probe \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/epp-normalize ./cmd/epp-normalize \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/envoy-normalize ./cmd/envoy-normalize \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/vllm-normalize ./cmd/vllm-normalize \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/p2p-runtime-normalize ./cmd/p2p-runtime-normalize \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/source-pressure-compile ./cmd/source-pressure-compile

FROM gcr.io/distroless/static-debian12:nonroot
ARG VELASERVE_REPOSITORY_COMMIT
ARG VELASERVE_BUILD_DIRTY
LABEL io.velaserve.component="velaserve" \
      io.velaserve.repository.commit="${VELASERVE_REPOSITORY_COMMIT}" \
      io.velaserve.build.dirty="${VELASERVE_BUILD_DIRTY}"
COPY --from=builder /out/ /app/
USER nonroot:nonroot
ENTRYPOINT ["/app/simfleet"]
