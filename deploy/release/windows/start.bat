@echo off
setlocal EnableExtensions
chcp 65001 >nul 2>&1

set "ROOT_DIR=%~dp0"
if "%ROOT_DIR:~-1%"=="\" set "ROOT_DIR=%ROOT_DIR:~0,-1%"

if exist "%ROOT_DIR%\.env" (
  for /f "usebackq eol=# tokens=1,* delims==" %%A in ("%ROOT_DIR%\.env") do (
    if not "%%~A"=="" set "%%~A=%%~B"
  )
)

if not defined SERVER_HOST set "SERVER_HOST=0.0.0.0"
if not defined SERVER_PORT set "SERVER_PORT=8080"
if not defined GIN_MODE set "GIN_MODE=release"
if not defined STATIC_DIR set "STATIC_DIR=%ROOT_DIR%\web"
if not defined DATA_DIR set "DATA_DIR=%ROOT_DIR%\data"
if not defined DB_DRIVER set "DB_DRIVER=sqlite"
if not defined DB_SQLITE_PATH set "DB_SQLITE_PATH=%DATA_DIR%\analy2api.db"
if not defined AGY_SESSION_RETENTION set "AGY_SESSION_RETENTION=stateless"
if not defined AGY_PROMPT_MODE set "AGY_PROMPT_MODE=default_intercept_continue"

if not exist "%DATA_DIR%" mkdir "%DATA_DIR%"

echo [Analy2API] Starting Multi-Platform AI Gateway on http://%SERVER_HOST%:%SERVER_PORT% ...
"%ROOT_DIR%\analy2api.exe"
endlocal
