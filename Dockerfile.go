# Build stage
FROM golang:1.24-alpine AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /server ./cmd/server

# Runtime stage (minimal — review only, no agent build tools)
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /server /server
COPY migrations/ /migrations/
COPY prompts/ /prompts/
COPY web/ /web/
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -q -O- http://localhost:8080/healthz || exit 1
ENTRYPOINT ["/server"]
