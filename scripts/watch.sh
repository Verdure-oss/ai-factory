#!/bin/bash
# ai-factory 热重载脚本（air 的替代方案）
# 用法: ./scripts/watch.sh
# 依赖: apt-get install inotify-tools

set -e

cd "$(dirname "$0")/.."

# 检查 inotifywait 是否安装
if ! command -v inotifywait &> /dev/null; then
    echo "⚠️  inotifywait 未安装，正在安装..."
    apt-get update -qq && apt-get install -y inotify-tools
fi

echo "👀 开始监听文件变更..."
echo "   监听目录: factory/"
echo "   文件类型: *.go"
echo "   排除目录: dist/, vendor/, tmp/, .git/"
echo ""
echo "💡 提示: 按 Ctrl+C 停止监听"
echo ""

# 首次运行
echo "🔨 首次编译..."
./scripts/dev.sh &
PID=$!

# 监听文件变更（只监听 .go 文件，通过过滤实现）
inotifywait -m -r -e modify,create,delete \
    --exclude '(dist/|vendor/|tmp/|\.git/|\.air\.toml)' \
    factory/ | while read -r directory events filename; do

    # 只处理 .go 文件
    if [[ "$filename" != *.go ]]; then
        continue
    fi

    echo ""
    echo "📝 检测到变更: ${directory}${filename}"
    echo "🔄 重新编译..."

    # 杀掉旧进程（包括子进程）
    if kill -0 $PID 2>/dev/null; then
        kill $PID 2>/dev/null
        pkill -P $PID 2>/dev/null || true
        sleep 2
    fi

    # 重新启动
    ./scripts/dev.sh &
    PID=$!

    echo "✅ 重启完成 (PID: $PID)"
    echo ""
done
