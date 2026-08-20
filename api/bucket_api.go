// bucket_api.go 桶模块接口:创建 / 列表 / 详情 / 修改 / 删除。
// 桶管理权限与"仅允许创建同等级桶"规则在 api 层预检。
package api

import (
	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/model"
	"orbitcloud/server"
)

// BucketAPI 桶模块接口(无状态;全部路由需 AuthMiddleware,桶管理权限在 api 层校验)。
type BucketAPI struct{}

// CreateBucket 创建桶(POST /buckets)。
// 请求体:{"name","description"?,"permission_level"?,"manage_permission_level"?};
// 等级缺省取创建者等级(管理等级缺省跟随访问等级),仅允许创建与自身相同等级的桶;
// 配置 bucket.admin_create_only=true 时仅管理员可创建桶。
func (BucketAPI) CreateBucket(c *gin.Context) {
	var req struct {
		Name                  string                 `json:"name"`
		Description           string                 `json:"description"`
		PermissionLevel       model.PermissionLevel  `json:"permission_level"`
		ManagePermissionLevel model.PermissionLevel  `json:"manage_permission_level"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 用户可用 + 等级与创建者一致
	user, err := permUserActive(ctx, userID)
	if err != nil {
		respondError(c, err)
		return
	}
	// 配置:仅管理员可建桶
	if core.GlobalConfig != nil && core.GlobalConfig.Bucket.AdminCreateOnly && !user.PermissionLevel.IsAdmin() {
		respondError(c, server.ErrForbidden)
		return
	}
	perm := req.PermissionLevel
	if perm < 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if perm == 0 {
		perm = user.PermissionLevel // 缺省自动取创建者等级
	}
	if perm != user.PermissionLevel {
		respondError(c, server.ErrForbidden)
		return
	}
	managePerm := req.ManagePermissionLevel
	if managePerm < 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}
	// 管理等级缺省跟随访问等级;非零时不得松于访问等级(管理须比访问更严或同级)
	if managePerm == 0 {
		managePerm = perm
	} else if managePerm > perm {
		respondError(c, server.ErrInvalidInput)
		return
	}

	arg := server.CreateBucketArg{
		OwnerID:               userID,
		Name:                  req.Name,
		Description:           req.Description,
		PermissionLevel:       perm,
		ManagePermissionLevel: managePerm,
	}

	bucket, err := server.CreateBucket(ctx, arg)
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, bucket)
}

// ListBuckets 返回当前用户可见的桶列表(GET /buckets);空列表返回 []。
func (BucketAPI) ListBuckets(c *gin.Context) {
	userID := currentUser(c)
	if _, err := permUserActive(c.Request.Context(), userID); err != nil {
		respondError(c, err)
		return
	}

	buckets, err := server.ListBuckets(c.Request.Context(), server.ListBucketsArg{UserID: userID})
	if err != nil {
		respondError(c, err)
		return
	}

	// nil 序列化前置为 []model.Bucket{},保证 JSON 为数组
	if buckets == nil {
		buckets = []model.Bucket{}
	}
	common.Success(c, buckets)
}

// GetBucket 返回桶详情(GET /buckets/:id);任意登录用户可查看。
func (BucketAPI) GetBucket(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}

	bucket, err := server.GetBucket(c.Request.Context(), server.GetBucketArg{ID: bucketID})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, bucket)
}

// UpdateBucket 更新桶(PUT /buckets/:id)。
// 请求体:{"description"?,"permission_level"?,"manage_permission_level"?,"quota"?,"status"?}
// (指针字段才更新);需满足桶管理权限(owner/管理员/管理等级满足);
// 非管理员不得把等级改得比自身权限更高。
func (BucketAPI) UpdateBucket(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 桶存在 → 桶管理权(owner/管理员/管理等级满足)
	bucket, err := server.GetBucket(ctx, server.GetBucketArg{ID: bucketID})
	if err != nil {
		respondError(c, err)
		return
	}
	if err := permCanManageBucket(ctx, userID, bucket); err != nil {
		respondError(c, err)
		return
	}

	// 解析请求体(本地 DTO 带 json tag,避免 server 层结构体直接绑定)
	var req struct {
		Description           string                 `json:"description"`
		PermissionLevel       *model.PermissionLevel `json:"permission_level"`
		ManagePermissionLevel *model.PermissionLevel `json:"manage_permission_level"`
		Quota                 *int64                 `json:"quota"`
		Status                *int                   `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 指针字段原样透传,缺失字段留 nil
	in := server.UpdateBucketInput{
		Description:           req.Description,
		PermissionLevel:       req.PermissionLevel,
		ManagePermissionLevel: req.ManagePermissionLevel,
		Quota:                 req.Quota,
		Status:                req.Status,
	}

	updated, err := server.UpdateBucket(ctx, server.UpdateBucketArg{OperatorID: userID, BucketID: bucketID, In: in})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, updated)
}

// DeleteBucket 删除桶(DELETE /buckets/:id,级联清理桶内文件,成功 204)。
// 删除经任务表持久化:先禁用桶,后台分批清理对象存储,中断后由启动扫描续跑。
func (BucketAPI) DeleteBucket(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 桶存在 → 桶管理权(owner/管理员/同级代管)
	bucket, err := server.GetBucket(ctx, server.GetBucketArg{ID: bucketID})
	if err != nil {
		respondError(c, err)
		return
	}
	if err := permCanManageBucket(ctx, userID, bucket); err != nil {
		respondError(c, err)
		return
	}

	if err := server.DeleteBucket(ctx, server.DeleteBucketArg{OperatorID: userID, BucketID: bucketID}); err != nil {
		respondError(c, err)
		return
	}

	c.Status(204)
}
