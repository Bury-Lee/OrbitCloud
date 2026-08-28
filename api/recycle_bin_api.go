// recycle_bin_api.go —— 回收站接口:查询自己的回收站(已删除且归属自己的文件/文件夹)。
// 删除/清除见 file_delete.go 与 server 回收站清理;本模块只做回收站浏览,
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

// recycleBinQuery 回收站通用查询(包级泛型函数):归属校验(查他人须管理员,经
// JWT claims 判定)+ 软删行过滤(Unscoped 越过 GORM 软删过滤)+ 归属过滤,
// 直接走 common.PageQuery。
func recycleBinQuery[T any](c *gin.Context, model T, arg ListRecycleBinArg) (*common.PaginatedResult[T], error) {
	// 归属校验:目标缺省 = 当前用户;查他人 → 必须管理员,否则拒绝
	userID := currentUser(c)
	targetID := arg.UserID
	if targetID == 0 {
		targetID = userID
	}
	if targetID != userID {
		claims := ClaimsFrom(c)
		if claims == nil || !claims.PermissionLevel.IsAdmin() {
			return nil, server.ErrForbidden
		}
	}

	opt := common.Option{
		PageInfo:     arg.PageInfo,
		DefaultOrder: "deleted_at DESC, id DESC",
	}
	opt.Filters = []func(*gorm.DB) *gorm.DB{
		func(db *gorm.DB) *gorm.DB {
			// 软删行 = 回收站条目,按归属者过滤
			return db.Unscoped().Where("deleted_at IS NOT NULL AND uploaded_by = ?", targetID)
		},
	}
	return common.PageQuery(model, opt, nil)
}

// ListRecycleFile 分页返回回收站文件(GET /recycle-bin/files?page=&page_size=&cursor=&key=&order=&user_id=):
// 已删除(软删)且归属目标的文件,deleted_at 倒序;统一走 common 分页封装
// (ShouldBindQuery 绑定参数 → PageQuery → WrapPageResponse 输出 PaginatedResult)。
func (RecycleBinAPI) ListRecycleFile(c *gin.Context) {
	var arg ListRecycleBinArg
	if err := c.ShouldBindQuery(&arg); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	res, err := recycleBinQuery(c, model.File{}, arg)
	if err != nil {
		respondError(c, err)
		return
	}
	common.WrapPageResponse(c, res)
}

// ListRecycleFolder 分页返回回收站文件夹(GET /recycle-bin/folders?page=&page_size=&cursor=&key=&order=&user_id=):
// 语义与 ListRecycleFile 相同,仅查询表为 folders。
func (RecycleBinAPI) ListRecycleFolder(c *gin.Context) {
	var arg ListRecycleBinArg
	if err := c.ShouldBindQuery(&arg); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	res, err := recycleBinQuery(c, model.Folder{}, arg)
	if err != nil {
		respondError(c, err)
		return
	}
	common.WrapPageResponse(c, res)
}
