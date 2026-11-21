# One image for every sidecar: `mg updater`, `mg watchdog`, `mg dbbackup`, `mg newsgate`.
# The updater needs git and the docker CLI; the others ignore them.
FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-s -o /mg ./cmd/mg

FROM docker:29.10.0-cli
RUN apk add --no-cache git
COPY --from=build /mg /usr/local/bin/mg
ENTRYPOINT ["mg"]
