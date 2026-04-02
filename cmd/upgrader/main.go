package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"github.com/apecloud/kubeblocks/cmd/upgrader/steps"
)

func main() {
	//解析命令行参数
	var modulesFlag string
	flag.StringVar(&modulesFlag, "modules", "core", "要执行的模块，逗号分隔: core,clickhouse,dbfix")
	flag.Parse()

	modules := make(map[string]bool)
	for _, m := range strings.FieldsFunc(modulesFlag, func(r rune) bool { return r == ',' || r == '，' }) {
		m = strings.TrimSpace(m)
		if m != "" {
			modules[m] = true
		}
	}
	modules["core"] = true

	//创建工作目录
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("获取用户主目录失败: %v\n", err)
		os.Exit(1)
	}

	workDir := filepath.Join(home, ".kb-upgrader", "workdir")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		fmt.Printf("创建工作目录失败: %v\n", err)
		os.Exit(1)
	}

	//打印启动bannner
	var moduleNames []string
	for m := range modules {
		moduleNames = append(moduleNames, m)
	}
	sort.Strings(moduleNames)

	fmt.Println("========================================")
	fmt.Println("  KubeBlocks Upgrader (v0.8.x → v0.9.3)")
	fmt.Printf("  模块: %s\n", strings.Join(moduleNames, ", "))
	fmt.Println("========================================")

	//注册所有步骤
	allSteps := steps.RegisterAll(modules)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := steps.RunOptions{
		Ctx:     ctx,
		WorkDir: workDir,
	}

	for i, step := range allSteps {
		fmt.Printf("\n[%d/%d] %s — %s\n", i+1, len(allSteps), step.Name(), step.Description())

		if skip, err := step.Check(opts); err != nil {
			fmt.Printf("       → 检查失败: %v\n", err)
			fmt.Println("\n前置检查失败，升级已暂停。")
			fmt.Println("请手动修复问题后重新运行以继续升级。")
			os.Exit(1)
		} else if skip {
			fmt.Println("       → 已符合预期，跳过")
			continue
		}

		if err := step.Run(opts); err != nil {
			fmt.Printf("       → 失败: %v\n", err)
			fmt.Println("\n此步骤失败，升级已暂停。")
			fmt.Println("请手动修复问题后重新运行以继续升级。")
			os.Exit(1)
		}

		fmt.Println("       → 成功")
	}

	fmt.Printf("\n========================================\n")
	fmt.Printf("  所有 %d 个步骤全部完成！\n", len(allSteps))
	fmt.Println("========================================")

	os.RemoveAll(workDir)
}
