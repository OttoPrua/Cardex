//go:build ignore

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/OttoPrua/Cardex/internal/board"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func main() {
	home := os.Getenv("VI15_HOME")
	if home == "" {
		fmt.Fprintln(os.Stderr, "VI15_HOME is required")
		os.Exit(1)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		panic(err)
	}
	if err := os.Setenv("CARDEX_HOME", home); err != nil {
		panic(err)
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if err := seed(db); err != nil {
		panic(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fmt.Printf("listening %s\n", ln.Addr())
	if err := http.Serve(ln, board.Handler()); err != nil {
		panic(err)
	}
}

func seed(db *store.Store) error {
	ctx := context.Background()
	projects := []struct{ id, title, project string }{
		{"shop-web", "商城前端", "a"},
		{"pay-svc", "支付服务", "b"},
		{"search-idx", "搜索索引", "c"},
		{"ops-tools", "运营工具", "d"},
	}
	for _, p := range projects {
		if err := db.CreateProject(ctx, &model.Project{ID: p.project, RepoPath: homeRepo()}); err != nil {
			return err
		}
		if err := db.CreateObjective(ctx, &model.Objective{
			ID: p.id, Project: p.project, Title: p.title,
			Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
		}); err != nil {
			return err
		}
	}
	cards := []*model.Card{
		{ID: "C-0148", Objective: "ops-tools", Title: "导出 CSV 加时区列", Kind: model.KindDev, Status: model.CardReady},
		{ID: "C-0151", Objective: "shop-web", Title: "移动端结算页适配", Kind: model.KindDev, Status: model.CardWaiting, WaitReason: "等待 12 分"},
		{ID: "C-0146", Objective: "pay-svc", Title: "支付回调幂等改造", Kind: model.KindDev, Status: model.CardRunning},
		{ID: "C-0149", Objective: "shop-web", Title: "订单列表虚拟滚动", Kind: model.KindDev, Status: model.CardHarvesting},
		{ID: "C-0158", Objective: "search-idx", Title: "索引分片迁移脚本", Kind: model.KindDev, Status: model.CardRunning},
		{ID: "C-0145", Objective: "pay-svc", Title: "退款流程状态机", Kind: model.KindDev, Status: model.CardReviewing},
		{ID: "C-0153", Objective: "search-idx", Title: "是否保留旧版 API v1？", Kind: model.KindDev, Status: model.CardNeedsInput},
		{ID: "C-0147", Objective: "shop-web", Title: "购物车并发锁", Kind: model.KindDev, Status: model.CardMergeQueued},
		{ID: "C-0143", Objective: "ops-tools", Title: "结算页文案统一", Kind: model.KindDev, Status: model.CardMerged},
		{ID: "C-0144", Objective: "ops-tools", Title: "优惠券叠加计算", Kind: model.KindDev, Status: model.CardFailed},
	}
	for _, c := range cards {
		if err := db.CreateCard(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func homeRepo() string {
	dir, err := os.MkdirTemp("", "vi15-repo")
	if err != nil {
		return "."
	}
	return dir
}
