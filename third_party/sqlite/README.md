# third_party/sqlite — SQLite amalgamation 头文件

## 用途

`sqlite-vec-go-bindings/cgo`（WeKnora Lite 模式的向量检索扩展）在编译时需要
`sqlite3.h`。Linux/macOS 上系统通常已装 `libsqlite3-dev`（`/usr/include/sqlite3.h`），
但 **Windows 原生编译（MinGW）没有系统 SQLite 头文件**，会导致：

```
./sqlite-vec.h:7:10: fatal error: sqlite3.h: No such file or directory
```

本目录提供该头文件，`scripts/dev.sh` 启动后端时会把它注入 CGO include path：

```bash
export CGO_CFLAGS="... -I\"$PROJECT_ROOT/third_party/sqlite\""
```

## 来源

- `sqlite3.h` —— 复制自 `github.com/mattn/go-sqlite3@v1.14.24` 的 `sqlite3-binding.h`
  （SQLite amalgamation 官方头文件，mattn 重命名）。与运行时实际链接的 SQLite 版本
  **完全一致**，避免头文件/库版本错配。
- `sqlite3ext.h` —— 同上来源，供扩展编译路径（非 `SQLITE_CORE` 模式）备用。

## 说明

- 头文件仅用于 **编译期函数声明**；运行时符号由 `mattn/go-sqlite3` 编译进二进制的
  SQLite core 提供（`-DSQLITE_CORE` 模式）。
- 此目录随仓库提交，Linux/macOS 构建同样无害（`-I` 指向一个存在的目录即可）。
