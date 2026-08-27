# smb-gateway 镜像: Rust 二进制
FROM debian:bookworm-slim

COPY bin/smb-gateway /usr/local/bin/smb-gateway

WORKDIR /opt/orbitcloud/smb-gateway
VOLUME ["/opt/orbitcloud/smb-gateway/logs"]

EXPOSE 445

ENTRYPOINT ["/usr/local/bin/smb-gateway"]
