# syntax=docker/dockerfile:1

# ---- Build stage -------------------------------------------------------------
# Compile a static binary in the official Go image.
FROM golang:1.22-alpine AS build

WORKDIR /src

# Download dependencies first so this layer is cached unless go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Build the server. CGO disabled -> a fully static binary that runs in scratch.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/fx-settlement ./cmd/server

# ---- Run stage ---------------------------------------------------------------
# Minimal final image: just the binary on top of a tiny base with CA certs.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates

COPY --from=build /bin/fx-settlement /bin/fx-settlement

EXPOSE 8080

ENTRYPOINT ["/bin/fx-settlement"]
