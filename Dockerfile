FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /sita-mcp ./cmd/sita-mcp

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /sita-mcp /sita-mcp
EXPOSE 8080
ENTRYPOINT ["/sita-mcp"]
