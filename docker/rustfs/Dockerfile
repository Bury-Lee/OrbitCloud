# rustfs 镜像: S3 对象存储单节点
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY bin/rustfs /usr/local/bin/rustfs

VOLUME ["/opt/rustfs/data"]

EXPOSE 9000

ENTRYPOINT ["/usr/local/bin/rustfs", "server", "--address", "0.0.0.0:9000", "/opt/rustfs/data"]
