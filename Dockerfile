FROM node:26.10.0-bookworm-slim@sha256:3ffc19ea878019d9e9ae8971732ad4a03cda44f167107173174b60ed7c65bed3 AS ui
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build

FROM golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0 AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=ui /src/internal/web/dist/ ./internal/web/dist/
ARG CHECKPOINT_VERSION=development
ARG CHECKPOINT_RELEASE_PUBLIC_KEY
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-X main.version=${CHECKPOINT_VERSION} -X main.releasePublicKey=${CHECKPOINT_RELEASE_PUBLIC_KEY}" -o /checkpoint ./cmd/checkpoint

FROM scratch
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=go-build /checkpoint /checkpoint
USER 10001:10001
ENTRYPOINT ["/checkpoint"]
