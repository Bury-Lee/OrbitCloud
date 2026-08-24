//! ============================================================================
//! types/tests.rs —— 契约层单元测试(与 Go 侧 gateway_test.go 对应)
//! ============================================================================
//!
//! 覆盖:常量与 Go 侧对齐(验收标准 3)、帧头编解码往返、控制面 body 布局、
//! 总操作 JSON 字段名(与 Go 侧 json tag 一致)。

use super::*; // 引入契约层全部公开项(待测代码)

/// 帧协议与操作码常量须与 Go 侧 protocol.go 完全一致(验收标准 3)。
#[test]
fn frame_constants_match_go_side() {
    assert_eq!(MAGIC, 0x4F435354, "magic \"OCST\" 与 Go 侧 Magic 一致"); // 魔数校验
    assert_eq!(VERSION, 1, "协议版本与 Go 侧 Version 一致"); // 版本校验
    assert_eq!(MAX_BODY_LEN, 16 << 20, "body 上限与 Go 侧 MaxBodyLen 一致"); // 上限校验
    assert_eq!(MSG_HELLO, 0x0001); // 握手消息值
    assert_eq!(MSG_AUTH_PUSH, 0x0015); // 推送消息值
    assert_eq!(MSG_OPERATE, 0x0201); // 总操作消息值
    assert_eq!(MSG_OPERATE_RESP, 0x0202); // 总响应消息值
    assert_eq!(MSG_ERR_RESP, 0x8001); // 兜底错误消息值
}

/// 操作码常量表须与 Go 侧 Code* 一一对应且不重复、不从 0 开始。
#[test]
fn operate_codes_match_go_side() {
    assert_eq!(CODE_INVALID, 0, "0 = 未填写哨兵"); // 哨兵值校验
    assert_eq!(CODE_AUTH_QUERY_USER, 1); // 查询用户
    assert_eq!(CODE_AUTH_QUERY_ACL, 2); // 查询 ACL
    assert_eq!(CODE_AUTH_SNAPSHOT, 3); // 快照
    assert_eq!(CODE_FILE_OPEN, 4); // 打开
    assert_eq!(CODE_FILE_READ, 5); // 读
    assert_eq!(CODE_FILE_WRITE, 6); // 写
    assert_eq!(CODE_FILE_FLUSH, 7); // 冲刷
    assert_eq!(CODE_FILE_STAT, 8); // 元信息
    assert_eq!(CODE_FILE_SET_TIMES, 9); // 时间戳
    assert_eq!(CODE_FILE_TRUNCATE, 10); // 截断
    assert_eq!(CODE_FILE_LIST_DIR, 11); // 列目录
    assert_eq!(CODE_FILE_CLOSE, 12); // 关闭
    assert_eq!(CODE_FILE_UNLINK, 13); // 删除
    assert_eq!(CODE_FILE_RENAME, 14); // 重命名
    let codes = [ // 全部操作码收集为数组
        CODE_AUTH_QUERY_USER,
        CODE_AUTH_QUERY_ACL,
        CODE_AUTH_SNAPSHOT,
        CODE_FILE_OPEN,
        CODE_FILE_READ,
        CODE_FILE_WRITE,
        CODE_FILE_FLUSH,
        CODE_FILE_STAT,
        CODE_FILE_SET_TIMES,
        CODE_FILE_TRUNCATE,
        CODE_FILE_LIST_DIR,
        CODE_FILE_CLOSE,
        CODE_FILE_UNLINK,
        CODE_FILE_RENAME,
    ];
    let mut seen = std::collections::BTreeSet::new(); // 有序集合:查重复
    for c in codes { // 逐个检查
        assert!(seen.insert(c), "操作码重复: {c}"); // 插入失败 = 重复
    }
}

/// 哨兵错误码须与 Go 侧 Err* 常量一一对应且不重复。
#[test]
fn error_codes_are_distinct() {
    let codes = [ // 全部错误码收集为数组
        ERR_OK,
        ERR_NOT_FOUND,
        ERR_ACCESS_DENIED,
        ERR_EXISTS,
        ERR_NOT_EMPTY,
        ERR_IS_DIRECTORY,
        ERR_NOT_A_DIRECTORY,
        ERR_IO,
        ERR_NOT_IMPL,
        ERR_BAD_AUTH,
        ERR_GATEWAY_DOWN,
        ERR_TIMEOUT,
        ERR_BAD_REQUEST,
    ];
    let mut seen = std::collections::BTreeSet::new(); // 查重集合
    for c in codes { // 逐个检查
        assert!(seen.insert(c), "错误码重复: {c}"); // 插入失败 = 重复
    }
    assert_eq!(seen.len(), codes.len(), "错误码集合大小与预期一致"); // 无重复则数量一致
}

/// 帧头结构字段顺序与 Go 侧 FrameHeader 一致(大端 16 字节)。
#[test]
fn frame_header_layout_is_stable() {
    let hdr = FrameHeader { // 构造样例帧头
        magic: MAGIC, // 魔数
        version: VERSION, // 版本
        flags: FLAG_NEED_REPLY, // 需响应标记
        msg_type: MSG_OPERATE, // 总操作类型
        seq: 1, // 序列号
        body_len: 0, // 空 body
    };
    assert_eq!(hdr.magic, MAGIC); // 字段值逐一断言
    assert_eq!(hdr.version, 1); // 版本
    assert_eq!(hdr.flags, FLAG_NEED_REPLY); // 标记
    assert_eq!(hdr.msg_type, MSG_OPERATE); // 类型
    assert_eq!(hdr.seq, 1); // 序号
    assert_eq!(hdr.body_len, 0); // 长度
}

/// 帧头编解码往返一致性(大端布局与 Go 侧一致)。
#[test]
fn frame_header_round_trip() {
    let hdr = FrameHeader { // 构造样例帧头
        magic: MAGIC, // 魔数
        version: VERSION, // 版本
        flags: FLAG_RESPONSE, // 响应标记
        msg_type: MSG_OPERATE_RESP, // 总响应类型
        seq: 42, // 序列号
        body_len: 1234, // 长度
    };
    let raw = encode_frame_header(&hdr); // 编码为 16B
    assert_eq!(raw.len(), HEADER_LEN); // 长度断言
    // 逐字节断言大端布局(magic/version/flags/msgType/seq/bodyLen)。
    assert_eq!(&raw[0..4], &0x4F435354u32.to_be_bytes()); // 魔数大端
    assert_eq!(raw[4], 1); // 版本
    assert_eq!(raw[5], FLAG_RESPONSE); // 标记
    assert_eq!(&raw[6..8], &MSG_OPERATE_RESP.to_be_bytes()); // 类型大端
    assert_eq!(&raw[8..12], &42u32.to_be_bytes()); // 序号大端
    assert_eq!(&raw[12..16], &1234u32.to_be_bytes()); // 长度大端

    let got = decode_frame_header(&raw).expect("decode ok"); // 解码回结构
    assert_eq!(got.magic, MAGIC); // 往返一致:魔数
    assert_eq!(got.seq, 42); // 往返一致:序号
    assert_eq!(got.body_len, 1234); // 往返一致:长度
}

/// 坏魔数/版本不匹配必须报错(协议硬契约)。
#[test]
fn decode_rejects_bad_magic_and_version() {
    let bad_magic = encode_frame_header(&FrameHeader { // 坏魔数帧头
        magic: 0xDEADBEEF, // 非法魔数
        version: VERSION, // 版本合法
        ..Default::default() // 其余默认
    });
    assert!(decode_frame_header(&bad_magic).is_err()); // 必须拒绝

    let bad_ver = encode_frame_header(&FrameHeader { // 坏版本帧头
        magic: MAGIC, // 魔数合法
        version: 99, // 非法版本
        ..Default::default() // 其余默认
    });
    assert!(decode_frame_header(&bad_ver).is_err()); // 必须拒绝
}

/// 控制面 body 布局:[4B jsonLen] + JSON + 流段(与 Go 侧一致)。
#[test]
fn operate_body_round_trip() {
    let req = OperateRequest { // 构造写请求
        code: CODE_FILE_WRITE, // 操作码:写
        write: Some(WriteArgs { // 写参数
            handle_id: 7, // 句柄
            offset: 99, // 偏移
        }),
        ..Default::default() // 其余字段 None
    };
    let stream: Vec<u8> = vec![1, 2, 3, 4]; // 模拟写数据流段
    let body = encode_operate_body(&req, &stream).expect("encode ok"); // 编码 body
    let (json_bytes, stream_out) = split_operate_body(&body).expect("split ok"); // 拆分 body
    assert_eq!(stream_out, stream); // 流段原样保留
    let got: OperateRequest = serde_json::from_slice(json_bytes).expect("json ok"); // JSON 段反序列化
    assert_eq!(got.code, CODE_FILE_WRITE); // 操作码一致
    assert_eq!(got.write.unwrap().handle_id, 7); // 参数一致
}

/// jsonLen 越界必须报错(硬契约)。
#[test]
fn split_operate_body_rejects_overflow() {
    let bad: Vec<u8> = vec![0xFF, 0xFF, 0xFF, 0xFF, 0x00]; // jsonLen=0xFFFFFFFF 远超实际长度
    assert!(split_operate_body(&bad).is_err()); // 必须拒绝
}

/// 总操作 JSON 序列化字段名与 Go 侧 json tag 一致(camelCase)。
#[test]
fn operate_request_json_keys_match_go_side() {
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
    let json = serde_json::to_value(&req).expect("json ok"); // 序列化为 JSON 值
    // 顶层必须为 code + 驼峰操作名(与 Go 侧 OperateRequest json tag 一致)。
    assert_eq!(json["code"], 4); // code 字段
    assert!(json.get("open").is_some(), "open 字段必须存在"); // 操作字段存在
    assert_eq!(json["open"]["nonDirectory"], true, "驼峰字段名与 Go 侧一致"); // 驼峰命名断言
    assert!(json.get("unlink").is_none(), "未填操作不得出现在 JSON 中"); // 未填操作不序列化
}
