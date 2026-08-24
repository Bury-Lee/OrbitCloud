#!/usr/bin/env bash
# ============================================================
#  OrbitCloud API 测试脚本 (Unix / Git Bash / WSL)
#  依赖: curl
#  用法: ./test/test.sh [base_url]
#  示例: ./test/test.sh http://localhost:8080
# ============================================================
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
TOKEN=""

echo "===== OrbitCloud API 测试 ====="
echo "Base URL: $BASE_URL"
echo ""

# --- 1. 登录获取 Token ---
echo "[1/6] 登录获取 Token …"
LOGIN_RESP=$(curl -s -X POST "$BASE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}')
echo "$LOGIN_RESP" | head -c 200
echo ""

# --- 2. 获取桶列表 ---
echo ""
echo "[2/6] 获取桶列表 …"
curl -s "$BASE_URL/api/v1/buckets" \
  -H "Authorization: Bearer $TOKEN" | head -c 500
echo ""

# --- 3. 文件搜索（桶根，前缀 "a"）---
echo ""
echo "[3/6] 文件搜索：桶根 + 前缀 'a' …"
curl -s "$BASE_URL/api/v1/buckets/1/files/search?key=a&page=1&page_size=10" \
  -H "Authorization: Bearer $TOKEN" | head -c 500
echo ""

# --- 4. 文件夹搜索（桶根，前缀 "b"）---
echo ""
echo "[4/6] 文件夹搜索：桶根 + 前缀 'b' …"
curl -s "$BASE_URL/api/v1/buckets/1/dirs/search?key=b&page=1&page_size=10" \
  -H "Authorization: Bearer $TOKEN" | head -c 500
echo ""

# --- 5. 文件搜索（指定目录路径）---
echo ""
echo "[5/6] 文件搜索：路径 'subdir' + 前缀 'readme' …"
curl -s "$BASE_URL/api/v1/buckets/1/files/search?path=subdir&key=readme&page=1&page_size=10" \
  -H "Authorization: Bearer $TOKEN" | head -c 500
echo ""

# --- 6. 文件夹搜索（指定 folder_id）---
echo ""
echo "[6/6] 文件夹搜索：folder_id=5 + 前缀 'data' …"
curl -s "$BASE_URL/api/v1/buckets/1/dirs/search?folder_id=5&key=data&page=1&page_size=10" \
  -H "Authorization: Bearer $TOKEN" | head -c 500
echo ""

echo ""
echo "===== 测试完成 ====="