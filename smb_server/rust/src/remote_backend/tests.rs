//! ============================================================================
//! remote_backend/tests.rs —— 单元测试 + mock Go 网关集成测试
//! ============================================================================
//!
//! 覆盖:
//! - intent_str / map_gateway_err 单元测试(与 Go 侧语义对齐);
//! - GatewayClient::connect → call 全链路集成测试(帧编解码、pending 应答表、
//!   流数据段、双向 HMAC 握手)——本地起 mock Go 网关验证。

use super::*; // 引入被测模块
use crate::remote_backend::backend::{intent_str, map_gateway_err}; // 工具函数
use crate::remote_backend::client::{hmac_sha256_hex, read_one_frame}; // 密码学工具与单帧读取
use crate::types::*; // 帧协议契约

/// OpenIntent → 帧协议字符串,须与 Go 侧 OpenArgs.Intent 一致。
#[test]
fn intent_str_matches_go_side() {
    use smb_server::OpenIntent; // 库的意图枚举
    assert_eq!(intent_str(OpenIntent::Open), "open"); // 只打开
    assert_eq!(intent_str(OpenIntent::Create), "create"); // 只创建
    assert_eq!(intent_str(OpenIntent::OpenOrCreate), "open_or_create"); // 打开或创建
    assert_eq!( // 覆盖或创建
        intent_str(OpenIntent::OverwriteOrCreate),
        "overwrite_or_create"
    );
    assert_eq!(intent_str(OpenIntent::Truncate), "truncate"); // 截断打开
}

/// 帧协议错误码映射到库的 SmbError(NTSTATUS 语义)。
#[test]
fn gateway_err_maps_to_smb_error() {
    use smb_server::SmbError; // 库的 SMB 错误
    assert!(matches!(map_gateway_err(ERR_NOT_FOUND), SmbError::NotFound)); // 不存在 → NotFound
    assert!(matches!( // 拒绝访问 → AccessDenied
        map_gateway_err(ERR_ACCESS_DENIED),
        SmbError::AccessDenied
    ));
    assert!(matches!(map_gateway_err(ERR_EXISTS), SmbError::Exists)); // 已存在 → Exists
    assert!(matches!(map_gateway_err(ERR_NOT_EMPTY), SmbError::NotEmpty)); // 非空 → NotEmpty
    assert!(matches!( // 是目录 → IsDirectory
        map_gateway_err(ERR_IS_DIRECTORY),
        SmbError::IsDirectory
    ));
    assert!(matches!( // 不是目录 → NotADirectory
        map_gateway_err(ERR_NOT_A_DIRECTORY),
        SmbError::NotADirectory
    ));
    assert!(matches!(map_gateway_err(ERR_IO), SmbError::Io(_))); // IO 错误 → Io
    assert!(matches!(map_gateway_err(0xFFFF), SmbError::Io(_))); // 未知码 → Io 兜底
}

/// 集成测试:本地 mock Go 网关(握手 + OPERATE 应答),验证
/// connect → call 全链路(帧编解码、pending 应答表、流数据段)。
#[tokio::test] // tokio 异步测试
async fn connect_and_call_against_mock_server() {
    use tokio::io::AsyncWriteExt; // 写原语(mock 服务端回帧)

    // ---- mock 服务端:绑定随机端口,模拟 Go 网关 ----
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0") // 绑定随机可用端口
        .await
        .expect("bind");
    let addr = listener.local_addr().expect("addr"); // 取实际端口
    // 共享密钥(两侧一致,≥16 字节)。
    let key: &[u8] = b"0123456789abcdef0123456789abcdef"; // 32B 测试密钥

    // 服务端任务:处理一次握手 + 一次 OPERATE 请求。
    let server_key = key.to_vec(); // 密钥拷贝进任务
    let server = tokio::spawn(async move { // 起 mock 服务端任务
        let (mut sock, _) = listener.accept().await.expect("accept"); // 接受客户端连接

        // ---- 握手 1:读 HELLO,校验摘要,回 HELLO_ACK ----
        let (hdr, body) = read_one_frame(&mut sock).await.expect("hello frame"); // 读 HELLO 帧
        assert_eq!(hdr.msg_type, MSG_HELLO); // 类型断言
        let hello: HelloRequest = serde_json::from_slice(&body).expect("hello json"); // 解析请求体
        let expect = hmac_sha256_hex( // 用共享密钥重算期望摘要
            &server_key,
            &format!("HELLO:{}:{}", hello.client_id, hello.nonce),
        );
        assert_eq!(hello.challenge_digest, expect, "服务端校验客户端摘要"); // 摘要一致 = 客户端持正确密钥
        let ack = HelloResponse { // 构造应答
            ok: true, // 握手成功
            server_nonce: "servernonce16b".into(), // 服务端随机数
            error: String::new(), // 无错误
        };
        let ack_body = serde_json::to_vec(&ack).expect("ack json"); // 序列化应答
        let ack_frame = encode_frame( // 组装应答帧
            FrameHeader {
                flags: FLAG_RESPONSE, // 响应标记
                msg_type: MSG_HELLO_ACK, // 应答类型
                seq: hdr.seq, // 带回请求序号
                ..Default::default() // 其余自动回填
            },
            &ack_body,
        );
        sock.write_all(&ack_frame).await.expect("write ack"); // 发出应答

        // ---- 握手 2:读 VERIFY,校验双向认证摘要 ----
        let (_, vbody) = read_one_frame(&mut sock).await.expect("verify frame"); // 读 VERIFY 帧
        let verify: HelloRequest = serde_json::from_slice(&vbody).expect("verify json"); // 解析验证体
        let expect_v = hmac_sha256_hex(&server_key, "VERIFY:servernonce16b"); // 重算双向认证摘要
        assert_eq!(verify.challenge_digest, expect_v, "服务端校验双向认证"); // 双向认证通过

        // ---- 业务:等 MSG_OPERATE,回 OPERATE_RESP ----
        let (hdr, body) = read_one_frame(&mut sock).await.expect("operate frame"); // 读 OPERATE 帧
        assert_eq!(hdr.msg_type, MSG_OPERATE); // 类型断言
        let (json_bytes, stream) = split_operate_body(&body).expect("split"); // 拆分请求 body
        assert_eq!(stream, b"hello data", "请求流数据段原样到达"); // 流段校验
        let req: OperateRequest = serde_json::from_slice(json_bytes).expect("req json"); // 解析请求 JSON
        assert_eq!(req.code, CODE_FILE_OPEN); // 操作码断言

        // 回总响应(open 结果)。
        let resp = OperateResponse { // 构造总响应
            code: CODE_FILE_OPEN, // 回显操作码
            open: Some(OpenResult { // 打开结果
                handle_id: 100, // 远程句柄
                is_dir: false, // 文件
                end_of_file: 11, // 大小
                exists: true, // 已存在
            }),
            ..Default::default() // 其余字段 None
        };
        let resp_body = encode_operate_body(&resp, &[]).expect("resp body"); // 编码响应 body
        let resp_frame = encode_frame( // 组装响应帧
            FrameHeader {
                flags: FLAG_RESPONSE, // 响应标记
                msg_type: MSG_OPERATE_RESP, // 总响应类型
                seq: hdr.seq, // 带回请求序号
                ..Default::default() // 其余自动回填
            },
            &resp_body,
        );
        sock.write_all(&resp_frame).await.expect("write resp"); // 发出响应
    });

    // ---- 客户端:连接并完成一次 open 调用 ----
    let (client, mut push_rx) =
        GatewayClient::connect(&addr.to_string(), key.to_vec(), "gw-test".into(), 16, std::time::Duration::from_secs(30)) // 连接+握手(管道容量 16,超时 30s)
            .await
            .expect("connect");
    let req = OperateRequest { // 构造打开请求
        code: CODE_FILE_OPEN, // 操作码:打开
        open: Some(OpenArgs { // 打开参数
            bucket_id: 1, // 桶
            path: "a/b.txt".into(), // 路径
            read: true, // 读权限
            write: false, // 写权限
            intent: "open".into(), // 意图
            directory: false, // 非目录打开
            non_directory: true, // 按文件打开
            delete_on_close: false, // 不关闭即删
        }),
        ..Default::default() // 其余字段 None
    };
    let body = encode_operate_body(&req, b"hello data").expect("body"); // 编码请求(带流段)
    let resp_body = client.call(MSG_OPERATE, body).await.expect("call ok"); // 发起 RPC 等应答
    let (json_bytes, stream_out) = split_operate_body(&resp_body).expect("split"); // 拆分响应
    assert!(stream_out.is_empty(), "open 响应无流段"); // 打开响应不应带数据
    let resp: OperateResponse = serde_json::from_slice(json_bytes).expect("resp json"); // 解析响应
    assert_eq!(resp.code, CODE_FILE_OPEN); // 操作码回显一致
    assert!(resp.err.is_none(), "无业务错误"); // 无错误
    let r = resp.open.expect("open result"); // 取打开结果
    assert_eq!(r.handle_id, 100); // 句柄一致
    assert_eq!(r.end_of_file, 11); // 大小一致
    let _ = &mut push_rx; // 推送接收端已接线(此处不消费)

    server.await.expect("server task ok"); // 等服务端任务完成(断言全部通过)
}
