# syntax=docker/dockerfile:1
#
# 多阶段构建：builder 只负责编译，runtime 不包含 Go 工具链。
# 目标：非 root、静态二进制、只暴露 API 端口。
#
# 国内网络可以覆盖基础镜像：
#   docker build --build-arg GO_IMAGE=docker.m.daocloud.io/library/golang:1.27.2-alpine \
#                --build-arg RUNTIME_IMAGE=docker.m.daocloud.io/library/alpine:3.22 .
ARG GO_IMAGE=golang:1.27.2-alpine
ARG RUNTIME_IMAGE=alpine:3.22
FROM ${GO_IMAGE} AS build

WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
        -ldflags "-s -w -X github.com/huakaifugui114514/working/internal/version.Version=${VERSION} -X github.com/huakaifugui114514/working/internal/version.Commit=${COMMIT} -X github.com/huakaifugui114514/working/internal/version.BuildTime=${BUILD_TIME}" \
        -o /out/apid ./cmd/apid && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/purge ./cmd/purge && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/embed ./cmd/embed && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/vector-clean ./cmd/vector-clean && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/mcp-server ./cmd/mcp-server && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/kafka-dispatcher ./cmd/kafka-dispatcher && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/kafka-embed ./cmd/kafka-embed

FROM ${RUNTIME_IMAGE}

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S knowflow && adduser -S -G knowflow knowflow

COPY --from=build /out/apid /out/migrate /out/purge /out/embed /out/vector-clean /out/mcp-server /out/kafka-dispatcher /out/kafka-embed /usr/local/bin/
COPY --from=build /src/configs/config.yaml /etc/knowflow/config.yaml

USER knowflow
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/apid"]
