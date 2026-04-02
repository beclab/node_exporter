FROM golang:1.23.8-alpine3.21 as builder

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum

COPY collector/ collector/
COPY node_exporter.go node_exporter.go
RUN go build -ldflags="-s -w" -a -o node_exporter node_exporter.go


FROM alpine:3.21
RUN apk add smartmontools pciutils nvme-cli util-linux
COPY --from=builder /workspace/node_exporter /bin/node_exporter

EXPOSE      9100
USER        nobody
ENTRYPOINT  [ "/bin/node_exporter" ]
