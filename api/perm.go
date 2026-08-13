// perm.go 权限校验原语:用户可用性、桶权限等级、条目级/祖先链可见性、归属与管理员放行。
// 数据源为 server 层只读查询;权限失败统一 ErrForbidden,资源不存在 ErrNotFound。
package api

import (
	"context"
	"strings"

	"orbitcloud/model"
	"orbitcloud/server"
)

// canAccess 权限等级判定(数值越小权限越高):userPerm <= needPerm 即可访问。
func canAccess(userPerm, needPerm int8) bool { return userPerm <= needPerm }

// permUserActive 校验用户存在且状态正常(Status==1),返回用户供后续判定复用。
func permUserActive(ctx context.Context, userID uint) (*model.User, error) {
	user, err := server.GetUser(ctx, server.GetUserArg{ID: userID})
	if err != nil {
		return nil, err
	}
	if user.Status != 1 {
		return nil, server.ErrForbidden // 账号禁用
	}
	return user, nil
}

// permBucketAccess 校验用户可访问桶(等级满足桶权限要求)。
func permBucketAccess(ctx context.Context, userID uint, bucket *model.Bucket) error {
	var user *model.User
	return permBucketAccessWith(ctx, userID, &user, bucket)
}

// permBucketAccessWith 桶权限判定(带用户懒加载缓存,供链式判定复用)。
func permBucketAccessWith(ctx context.Context, userID uint, user **model.User, bucket *model.Bucket) error {
	u, err := ensurePermUser(ctx, userID, user)
	if err != nil {
		return err
	}
	if !canAccess(u.PermissionLevel, bucket.PermissionLevel) {
		return server.ErrForbidden // 权限不足
	}
	return nil
}

// ensurePermUser 懒加载当前用户(存在 + 状态正常);缓存指针已填充时零查询。
func ensurePermUser(ctx context.Context, userID uint, user **model.User) (*model.User, error) {
	if *user != nil {
		return *user, nil
	}
	u, err := permUserActive(ctx, userID)
	if err != nil {
		return nil, err
	}
	*user = u
	return u, nil
}

// permItemAccessWith 条目级可见性判定(带用户/组成员懒加载缓存):
// 空可见组不限制;上传者本人或管理员(权限<=1)可见;其余按 server.ItemVisibleRule 判定。
func permItemAccessWith(ctx context.Context, userID uint, user **model.User, groups *[]uint, visibleToGroups string, uploadedBy uint) error {
	if strings.TrimSpace(visibleToGroups) == "" {
		return nil // 空 = 不限制
	}
	u, err := ensurePermUser(ctx, userID, user)
	if err != nil {
		return err
	}
	if uploadedBy == userID {
		return nil
	}
	if u.PermissionLevel <= 1 {
		return nil // 管理员可见一切(含受限条目)
	}
	// 组内成员判定(懒加载,一次查询)
	if *groups == nil {
		gs, err := server.UserGroupIDs(ctx, server.UserGroupIDsArg{UserID: userID})
		if err != nil {
			return err
		}
		*groups = gs
	}
	return server.ItemVisibleRule(userID, visibleToGroups, uploadedBy, u, *groups)
}

// permAncestorsAccessWith 自底向上校验祖先链:任一祖先已删 → ErrNotFound(404 优先),
// 每层同时做条目级可见性判定;用户/组成员数据跨层共享,全程最多一次查询。
// folderID==0(桶根)无条目行,直接通过。
func permAncestorsAccessWith(ctx context.Context, userID uint, user **model.User, groups *[]uint, bucketID, folderID uint) error {
	cur := folderID
	seen := map[uint]bool{} // 环保护(数据异常时防死循环)
	for cur != 0 && !seen[cur] {
		seen[cur] = true
		f, err := server.GetFolder(ctx, server.GetFolderArg{BucketID: bucketID, FolderID: cur})
		if err != nil {
			return err
		}
		if !f.Isable {
			return server.ErrNotFound // 目录已删 → 不可达(404 防探测)
		}
		if err := permItemAccessWith(ctx, userID, user, groups, f.VisibleToGroups, f.UploadedBy); err != nil {
			return err
		}
		cur = f.ParentID
	}
	return nil
}

// permOwnerOrAdmin 归属校验:userID 为 ownerID 或操作者为管理员(权限 <= 1)时通过。
func permOwnerOrAdmin(ctx context.Context, userID, ownerID uint) error {
	if userID == ownerID {
		return nil
	}
	user, err := permUserActive(ctx, userID)
	if err != nil {
		return err
	}
	if user.PermissionLevel <= 1 {
		return nil // 管理员放行
	}
	return server.ErrForbidden
}

// permCanManageBucket 桶管理权限(UpdateBucket/DeleteBucket 共用):
// owner 直接通过;管理员(权限<=1)可代管任意桶;其余要求操作者权限不高于 owner。
func permCanManageBucket(ctx context.Context, operatorID uint, bucket *model.Bucket) error {
	if bucket.OwnerID == operatorID {
		return nil
	}
	op, err := permUserActive(ctx, operatorID)
	if err != nil {
		return err
	}
	if op.PermissionLevel <= 1 {
		return nil // 管理员无视 owner/桶权限,直接代管
	}
	owner, err := server.GetUser(ctx, server.GetUserArg{ID: bucket.OwnerID})
	if err != nil {
		return err // owner 已被删除 → 非管理员无法操作其桶
	}
	if op.PermissionLevel > owner.PermissionLevel {
		return server.ErrForbidden // 权限低于 owner,不可代管
	}
	return nil
}

// permGroupVisible 组成员可见性:管理员(权限<=1)或本人为组内成员时通过。
func permGroupVisible(ctx context.Context, userID, groupID uint) error {
	user, err := permUserActive(ctx, userID)
	if err != nil {
		return err
	}
	if user.PermissionLevel <= 1 {
		return nil // 管理员可见一切组
	}
	groups, err := server.UserGroupIDs(ctx, server.UserGroupIDsArg{UserID: userID})
	if err != nil {
		return err
	}
	for _, gid := range groups {
		if gid == groupID {
			return nil
		}
	}
	return server.ErrForbidden
}

// 组合预检(可行性走 server,权限在本层)

// precheckFileRead 文件读取类操作预检(下载/预览/元数据/删除/复制源/移动源等共用):
// 桶可用 + 文件存在 + 祖先链(Isable/ACL)+ 条目 ACL + 桶权限,返回文件元数据。
// 用户/组成员数据沿链懒加载共享,全程最多一次查询。
func precheckFileRead(ctx context.Context, userID, bucketID, fileID uint) (*model.File, error) {
	bucket, err := server.CheckBucketUsable(ctx, server.CheckBucketUsableArg{BucketID: bucketID})
	if err != nil {
		return nil, err
	}
	file, err := server.GetFile(ctx, server.GetFileArg{BucketID: bucketID, FileID: fileID})
	if err != nil {
		return nil, err
	}
	var user *model.User
	var groups []uint
	if err := permAncestorsAccessWith(ctx, userID, &user, &groups, bucketID, file.FolderID); err != nil {
		return nil, err
	}
	if err := permItemAccessWith(ctx, userID, &user, &groups, file.VisibleToGroups, file.UploadedBy); err != nil {
		return nil, err
	}
	if err := permBucketAccessWith(ctx, userID, &user, bucket); err != nil {
		return nil, err
	}
	return file, nil
}

// precheckFolderRead 文件夹读取类操作预检(元数据/删除/复制源/移动源等共用):
// 桶可用 + 目录存在 + 祖先链(Isable/ACL)+ 条目 ACL + 桶权限,返回文件夹元数据。
func precheckFolderRead(ctx context.Context, userID, bucketID, folderID uint) (*model.Folder, error) {
	bucket, err := server.CheckBucketUsable(ctx, server.CheckBucketUsableArg{BucketID: bucketID})
	if err != nil {
		return nil, err
	}
	folder, err := server.GetFolder(ctx, server.GetFolderArg{BucketID: bucketID, FolderID: folderID})
	if err != nil {
		return nil, err
	}
	var user *model.User
	var groups []uint
	if err := permAncestorsAccessWith(ctx, userID, &user, &groups, bucketID, folder.ParentID); err != nil {
		return nil, err
	}
	if err := permItemAccessWith(ctx, userID, &user, &groups, folder.VisibleToGroups, folder.UploadedBy); err != nil {
		return nil, err
	}
	if err := permBucketAccessWith(ctx, userID, &user, bucket); err != nil {
		return nil, err
	}
	return folder, nil
}

// precheckBucketWrite 桶写操作预检(上传/建目录/复制移动目标桶等):桶可用 + 桶权限等级。
func precheckBucketWrite(ctx context.Context, userID, bucketID uint) (*model.Bucket, error) {
	bucket, err := server.CheckBucketUsable(ctx, server.CheckBucketUsableArg{BucketID: bucketID})
	if err != nil {
		return nil, err
	}
	if err := permBucketAccess(ctx, userID, bucket); err != nil {
		return nil, err
	}
	return bucket, nil
}

// precheckBucketRead 桶读操作预检(列表/浏览等只读场景),与写预检同口径。
func precheckBucketRead(ctx context.Context, userID, bucketID uint) (*model.Bucket, error) {
	return precheckBucketWrite(ctx, userID, bucketID)
}
