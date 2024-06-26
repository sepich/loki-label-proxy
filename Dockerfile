FROM golang:1.20 as builder
RUN mkdir /configs
WORKDIR /app
COPY go.* ./
RUN go mod download
COPY *.go Makefile .git ./
RUN make build

FROM gcr.io/distroless/static-debian11:nonroot
COPY --from=builder /configs /configs
COPY --from=builder /app/loki-label-proxy /loki-label-proxy
ENTRYPOINT ["/loki-label-proxy"]
CMD ["--help"]
