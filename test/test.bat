@echo off
chcp 65001 >nul
REM ============================================================
REM  OrbitCloud API 测试脚本 (Windows)
REM  依赖: curl (已内置于 Windows 10 1803+ / PowerShell)
REM  用法: test\test.bat [base_url]
REM  示例: test\test.bat http://localhost:8080
REM ============================================================

set BASE_URL=%1
if "%BASE_URL%"=="" set BASE_URL=http://localhost:8080

set TOKEN=

REM --- 1. 登录获取 token ---
echo [1/6] 登录获取 Token …
for /f "usebackq tokens=*" %%a in (`curl -s -X POST "%BASE_URL%/api/v1/auth/login" ^
  -H "Content-Type: application/json" ^
  -d "{\"username\":\"admin\",\"password\":\"admin123\"}" ^
  ^| "%SystemRoot%\System32\findstr.exe" "token"`) do (
  echo %%a
)

REM --- 2. 获取桶列表（并记住第一个桶 ID）---
echo.
echo [2/6] 获取桶列表 …
curl -s "%BASE_URL%/api/v1/buckets" -H "Authorization: Bearer %TOKEN%"
echo.

REM --- 3. 文件搜索（桶根，前缀 "a"）---
echo [3/6] 文件搜索：桶根 + 前缀 "a" …
curl -s "%BASE_URL%/api/v1/buckets/1/files/search?key=a&page=1&page_size=10" -H "Authorization: Bearer %TOKEN%"
echo.

REM --- 4. 文件夹搜索（桶根，前缀 "b"）---
echo [4/6] 文件夹搜索：桶根 + 前缀 "b" …
curl -s "%BASE_URL%/api/v1/buckets/1/dirs/search?key=b&page=1&page_size=10" -H "Authorization: Bearer %TOKEN%"
echo.

REM --- 5. 文件搜索（指定目录路径）---
echo [5/6] 文件搜索：路径 "subdir" + 前缀 "readme" …
curl -s "%BASE_URL%/api/v1/buckets/1/files/search?path=subdir&key=readme&page=1&page_size=10" -H "Authorization: Bearer %TOKEN%"
echo.

REM --- 6. 文件夹搜索（指定 folder_id）---
echo [6/6] 文件夹搜索：folder_id=5 + 前缀 "data" …
curl -s "%BASE_URL%/api/v1/buckets/1/dirs/search?folder_id=5&key=data&page=1&page_size=10" -H "Authorization: Bearer %TOKEN%"
echo.

echo.
echo ===== 测试完成 =====