FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o watcher .

FROM alpine:3
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/watcher /watcher
ENTRYPOINT ["/watcher"]
