FROM golang:1.27-bookworm AS build
RUN CGO_ENABLED=0 go install tailscale.com/cmd/derper@v1.103.0-pre.0.20260830144538-72780705eda8

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /go/bin/derper /app/derper
USER nonroot:nonroot
EXPOSE 80 443 3478/udp
ENTRYPOINT ["/app/derper"]
