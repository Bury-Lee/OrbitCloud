// storage.go —— 对象存储抽象与实现:实体数据只存对象存储,对象键 = 文件记录主键 ID,
// 桶名 = utils.BucketEncoder(桶ID)。全局单例 core.Storage 由 appinit.InitStorage 按
// config.Storage.Driver 构造。驱动:
//   - "s3":基于 minio-go v7 对接 S3 协议服务(RustFS / MinIO;path-style;隐式建桶)
//   - "local":本地目录驱动(开发/测试零依赖,目录即 bucket)
//
// Put 隐式建桶:不做存在性预查,直接 MakeBucket(幂等,已存在视为成功)。
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"orbitcloud/config"
)

// ErrObjectNotFound 对象不存在(哨兵;server 层经 errors.Is 判定:删除幂等 / 下载 404)。
var ErrObjectNotFound = errors.New("object storage: object not found")

// ObjectStorage 对象存储接口(server 层唯一访问实体数据的通道)。
// 约定:
//   - bucket 由调用方传入(如 utils.BucketEncoder(桶ID) 映射名);
//   - Put 隐含建桶语义;Get 返回的流由调用方负责 Close;
//   - 对象不存在时 Get/Delete 返回 ErrObjectNotFound(删除幂等);
//   - DeleteBucket 整桶删除,桶不存在视为成功(幂等,供删除任务续跑重试)。
type ObjectStorage interface {
	Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	// GetRange 范围读:读取对象 [start, end] 闭区间字节(S3 SetRange 语义),
	// 供随机读/断点续传/音视频流式播放使用。约定:0 ≤ start ≤ end,
	// 区间越界由调用方保证(HTTP 层按 FileSize 校验后 416);返回流只含该区间,
	// 读满 (end-start+1) 字节即 EOF;调用方负责 Close;对象不存在 → ErrObjectNotFound。
	GetRange(ctx context.Context, bucket, key string, start, end int64) (io.ReadCloser, error)
	Delete(ctx context.Context, bucket, key string) error
	// DeleteBucket 整桶删除:清空该 bucket 下全部对象并移除 bucket。
	// 桶不存在 → 视为成功;对象删除失败 → 返回错误(重试可继续清空)。
	DeleteBucket(ctx context.Context, bucket string) error
	// Ping 连通性自检(启动期调用):s3 → ListBuckets;local → 根目录可写检查。
	Ping(ctx context.Context) error
}

// NewObjectStorage 按驱动名构造对象存储实现("s3"|"local",其他 → 错误)。
func NewObjectStorage(driver string, cfg *config.Storage) (ObjectStorage, error) {
	switch strings.ToLower(driver) {
	case "s3":
		return newS3Storage(cfg)
	case "local":
		return newLocalStorage(cfg)
	default:
		return nil, fmt.Errorf("storage: unsupported driver %q (s3|local)", driver)
	}
}

// ============ s3 驱动(minio-go v7) ============

type s3Storage struct {
	client *minio.Client
}

// newS3Storage 构造 S3 驱动客户端。endpoint 允许带 scheme(http://…),此处剥离为 host:port。
func newS3Storage(cfg *config.Storage) (*s3Storage, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimSuffix(endpoint, "/")
	if endpoint == "" {
		return nil, errors.New("storage: s3 driver requires storage.endpoint")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: new s3 client: %w", err)
	}
	return &s3Storage{client: client}, nil
}

// ensureBucket 隐式建桶(幂等):MakeBucket 对已存在桶返回成功或 "已拥有" 类错误,均视为成功。
func (s *s3Storage) ensureBucket(ctx context.Context, bucket string) error {
	if err := s.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
		var resp minio.ErrorResponse
		if errors.As(err, &resp) && (resp.Code == "BucketAlreadyOwnedByYou" || resp.Code == "BucketAlreadyExists") {
			return nil
		}
		return fmt.Errorf("storage: make bucket %q: %w", bucket, err)
	}
	return nil
}

func (s *s3Storage) Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	if err := s.ensureBucket(ctx, bucket); err != nil {
		return err
	}
	_, err := s.client.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("storage: put %s/%s: %w", bucket, key, err)
	}
	return nil
}

func (s *s3Storage) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s.mapGetError(err, bucket, key)
	}
	// GetObject 的错误多为惰性返回,Stat 提前暴露(对象不存在等)
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, s.mapGetError(err, bucket, key)
	}
	return obj, nil
}

// GetRange s3 驱动范围读:SetRange 由对象存储服务端裁剪字节区间,不整对象传输。
// 存在性预检与区间读取用两个独立请求:minio-go 在同一对象上先 Stat 再 Read 会
// 清掉 Range 头导致返回全量对象,故先不带 Range 预检,再发带 Range 的独立请求。
func (s *s3Storage) GetRange(ctx context.Context, bucket, key string, start, end int64) (io.ReadCloser, error) {
	statObj, err := s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s.mapGetError(err, bucket, key)
	}
	if _, err := statObj.Stat(); err != nil {
		statObj.Close()
		return nil, s.mapGetError(err, bucket, key)
	}
	statObj.Close()
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(start, end); err != nil {
		return nil, fmt.Errorf("storage: set range %s/%s [%d,%d]: %w", bucket, key, start, end, err)
	}
	obj, err := s.client.GetObject(ctx, bucket, key, opts)
	if err != nil {
		return nil, s.mapGetError(err, bucket, key)
	}
	return obj, nil
}

func (s *s3Storage) Delete(ctx context.Context, bucket, key string) error {
	// RemoveObject 对不存在的对象返回 nil(幂等)
	err := s.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("storage: delete %s/%s: %w", bucket, key, err)
	}
	return nil
}

// DeleteBucket 整桶删除:S3 不允许直接删非空桶,先分页枚举全部对象逐个删除,再移除桶。
// 桶不存在 → 幂等成功;单对象删除失败 → 返回错误(续跑重试会继续清空)。
func (s *s3Storage) DeleteBucket(ctx context.Context, bucket string) error {
	for {
		count := 0
		for obj := range s.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if obj.Err != nil {
				var resp minio.ErrorResponse
				if errors.As(obj.Err, &resp) && resp.Code == "NoSuchBucket" {
					return nil // 桶不存在 → 幂等成功
				}
				return fmt.Errorf("storage: list bucket %q: %w", bucket, obj.Err)
			}
			if err := s.client.RemoveObject(ctx, bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
				return fmt.Errorf("storage: delete %s/%s: %w", bucket, obj.Key, err)
			}
			count++
		}
		if count == 0 {
			break // 本轮无对象(或桶不存在已在上方返回)
		}
	}
	if err := s.client.RemoveBucket(ctx, bucket); err != nil {
		var resp minio.ErrorResponse
		if errors.As(err, &resp) && resp.Code == "NoSuchBucket" {
			return nil
		}
		return fmt.Errorf("storage: remove bucket %q: %w", bucket, err)
	}
	return nil
}

// mapGetError 把 minio 错误映射为哨兵:404 → ErrObjectNotFound,其余原样包装。
func (s *s3Storage) mapGetError(err error, bucket, key string) error {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) && resp.StatusCode == 404 {
		return ErrObjectNotFound
	}
	return fmt.Errorf("storage: get %s/%s: %w", bucket, key, err)
}

// Ping 连通性自检:ListBuckets 验证 endpoint 可达与凭据有效。
func (s *s3Storage) Ping(ctx context.Context) error {
	if _, err := s.client.ListBuckets(ctx); err != nil {
		return fmt.Errorf("storage: ping s3 endpoint: %w", err)
	}
	return nil
}

// ============ local 驱动(开发/测试) ============

type localStorage struct {
	root string // 根目录;bucket 为根下子目录,key 为相对路径
}

func newLocalStorage(cfg *config.Storage) (*localStorage, error) {
	root := strings.TrimSpace(cfg.Endpoint)
	if root == "" {
		root = "./data/local"
	}
	// endpoint 若误填了 URL 形式,取其 path 段作为目录
	if strings.Contains(root, "://") {
		if i := strings.Index(root, "://"); i >= 0 {
			root = root[i+3:]
		}
	}
	root = strings.TrimSuffix(root, "/")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create local root %q: %w", root, err)
	}
	return &localStorage{root: root}, nil
}

// resolve 把 bucket/key 解析为绝对路径(防穿越:拒绝 ".." 段与绝对路径)。
func (l *localStorage) resolve(bucket, key string) (string, error) {
	if bucket == "" || key == "" || strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("storage: invalid bucket/key %q/%q", bucket, key)
	}
	clean := filepath.Clean(filepath.Join(l.root, bucket, filepath.FromSlash(key)))
	rootAbs, _ := filepath.Abs(l.root)
	cleanAbs, err := filepath.Abs(clean)
	if err != nil {
		return "", err
	}
	if cleanAbs != rootAbs && !strings.HasPrefix(cleanAbs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("storage: path escape %q", cleanAbs)
	}
	return cleanAbs, nil
}

func (l *localStorage) Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	path, err := l.resolve(bucket, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("storage: mkdir %q: %w", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("storage: create %q: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("storage: write %q: %w", path, err)
	}
	return nil
}

func (l *localStorage) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	path, err := l.resolve(bucket, key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("storage: open %q: %w", path, err)
	}
	return f, nil
}

// GetRange local 驱动范围读:os.Open + Seek(start) + LimitReader(end-start+1)。
// start ≤ size 由上层保证;若文件实际更短,后续读 EOF 由 io.Copy 自然结束(HTTP 层 416 兜底)。
func (l *localStorage) GetRange(ctx context.Context, bucket, key string, start, end int64) (io.ReadCloser, error) {
	path, err := l.resolve(bucket, key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("storage: open %q: %w", path, err)
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		f.Close()
		return nil, fmt.Errorf("storage: seek %q: %w", path, err)
	}
	return &limitedReadCloser{f: f, inner: io.LimitReader(f, end-start+1)}, nil
}

// limitedReadCloser 区间读包装流:读取委托给 LimitReader(读满 length 即 EOF),Close 关闭底层句柄。
type limitedReadCloser struct {
	f     *os.File
	inner io.Reader
}

func (r *limitedReadCloser) Read(p []byte) (int, error) { return r.inner.Read(p) }
func (r *limitedReadCloser) Close() error               { return r.f.Close() }

func (l *localStorage) Delete(ctx context.Context, bucket, key string) error {
	path, err := l.resolve(bucket, key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil // 幂等:对象不存在视为已删
		}
		return fmt.Errorf("storage: remove %q: %w", path, err)
	}
	return nil
}

// DeleteBucket 整桶删除:移除本地 bucket 目录(递归清空对象),目录不存在视为成功。
func (l *localStorage) DeleteBucket(ctx context.Context, bucket string) error {
	// bucket 名由 utils.BucketEncoder(数字编码)生成,不可能含分隔符;显式拒绝防误删上层目录
	if bucket == "" || strings.ContainsAny(bucket, "/\\") {
		return fmt.Errorf("storage: invalid bucket %q", bucket)
	}
	if err := os.RemoveAll(filepath.Join(l.root, bucket)); err != nil {
		return fmt.Errorf("storage: remove bucket %q: %w", bucket, err)
	}
	return nil
}

// Ping 连通性自检:根目录存在且可写(写临时文件后删除)。
func (l *localStorage) Ping(ctx context.Context) error {
	if err := os.MkdirAll(l.root, 0o755); err != nil {
		return fmt.Errorf("storage: local root %q: %w", l.root, err)
	}
	f, err := os.CreateTemp(l.root, ".ping-*")
	if err != nil {
		return fmt.Errorf("storage: local root %q not writable: %w", l.root, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// ============ 编译期接口断言 ============

var _ ObjectStorage = (*s3Storage)(nil)
var _ ObjectStorage = (*localStorage)(nil)
