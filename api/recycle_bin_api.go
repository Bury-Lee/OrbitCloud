// recycle_bin_api.go —— 回收站接口:查询自己的回收站(已删除且归属自己的文件/文件夹)。
// 直接走 common.PageQuery,不经过 server 层。
package api

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"orbitcloud/common"
	"orbitcloud/model"
	"orbitcloud/server"
)

// RecycleBinAPI 回收站模块接口(无状态)。
type RecycleBinAPI struct{}

// ListRecycleBinArg 回收站查询参数。
type ListRecycleBinArg struct {
	common.PageInfo      // 查询参数(page/page_size/cursor/key/order)
	UserID          uint // 目标用户 users.id;0 = 当前用户;非管理员只可查询自己的
}

// ListRecycleFile 分页返回自己的回收站文件(GET /recycle-bin/files?page=&page_size=&cursor=&key=&order=&user_id=):
// 已删除(软删)且归属目标的文件,deleted_at 倒序;直接 common.PageQuery,不经过 server 层。
func (RecycleBinAPI) ListRecycleFile(c *gin.Context) {
	var arg ListRecycleBinArg
	if err := c.ShouldBindQuery(&arg); err != nil {
		respondError(c, common.ErrInvalidInput)
		return
	}

	// 归属校验:目标缺省 = 当前用户;查他人 → 从 JWT claims 检查是否为管理员,不是则拒绝
	userID := currentUser(c)
	targetID := arg.UserID
	if targetID == 0 {
		targetID = userID
	}
	if targetID != userID {
		if claims := ClaimsFrom(c); claims == nil || !claims.PermissionLevel.IsAdmin() {
			respondError(c, common.ErrForbidden)
			return
		}
	}

	// 直接查询
	opt := common.Option{
		PageInfo:     arg.PageInfo,
		DefaultOrder: "deleted_at DESC, id DESC",
		Filters: []func(*gorm.DB) *gorm.DB{
			func(db *gorm.DB) *gorm.DB {
				// 软删行 = 回收站条目(Unscoped 越过 GORM 软删过滤),按归属者过滤
				return db.Unscoped().Where("deleted_at IS NOT NULL AND uploaded_by = ?", targetID)
			},
		},
	}
	res, err := common.PageQuery(model.File{}, opt, nil)
	if err != nil {
		respondError(c, err)
		return
	}
	common.WrapPageResponse(c, res)
}

// DeleteRecycleFile 永久删除回收站文件(DELETE /recycle-bin/files/:id,成功 204):
// 提前清除(不等保留期)——删对象 + 硬删记录 + 归还桶/用户用量 + 级联删分享。
// 校验都在本层:条目须在回收站(404),仅归属者或管理员可删(403)。
func (RecycleBinAPI) DeleteRecycleFile(c *gin.Context) {
	fileID := parseIDParam(c, "id")
	if fileID == 0 {
		return
	}

	// 可行性预检:文件存在且在回收站
	file, err := server.GetRecycledFile(c.Request.Context(), server.GetRecycledFileArg{FileID: fileID})
	if err != nil {
		respondError(c, err)
		return
	}
	// 归属校验:归属者本人,或管理员(JWT claims 判定)
	operatorID := currentUser(c)
	if file.UploadedBy != operatorID {
		if claims := ClaimsFrom(c); claims == nil || !claims.PermissionLevel.IsAdmin() {
			respondError(c, common.ErrForbidden)
			return
		}
	}

	if err := server.DeleteRecycledFile(c.Request.Context(), server.DeleteRecycledFileArg{
		OperatorID: operatorID,
		FileID:     fileID,
	}); err != nil {
		respondError(c, err)
		return
	}
	c.Status(204)
}

// ListRecycleFolder 分页返回自己的回收站文件夹(GET /recycle-bin/folders?page=&page_size=&cursor=&key=&order=&user_id=):
// 语义与 ListRecycleFile 相同,仅查询表为 folders;直接 common.PageQuery,不经过 server 层。
func (RecycleBinAPI) ListRecycleFolder(c *gin.Context) {
	var arg ListRecycleBinArg
	if err := c.ShouldBindQuery(&arg); err != nil {
		respondError(c, common.ErrInvalidInput)
		return
	}

	// 归属校验:目标缺省 = 当前用户;查他人 → 从 JWT claims 检查是否为管理员,不是则拒绝
	userID := currentUser(c)
	targetID := arg.UserID
	if targetID == 0 {
		targetID = userID
	}
	if targetID != userID {
		if claims := ClaimsFrom(c); claims == nil || !claims.PermissionLevel.IsAdmin() {
			respondError(c, common.ErrForbidden)
			return
		}
	}

	// 直接查询,不用调用 server 层
	opt := common.Option{
		PageInfo:     arg.PageInfo,
		DefaultOrder: "deleted_at DESC, id DESC",
		Filters: []func(*gorm.DB) *gorm.DB{
			func(db *gorm.DB) *gorm.DB {
				return db.Unscoped().Where("deleted_at IS NOT NULL AND uploaded_by = ?", targetID)
			},
		},
	}
	res, err := common.PageQuery(model.Folder{}, opt, nil)
	if err != nil {
		respondError(c, err)
		return
	}
	common.WrapPageResponse(c, res)
}
