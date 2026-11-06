// Package syllabus owns the built-in interview outlines (题纲). An outline is
// a stable list of knowledge points with expected coverage points so the
// coverage gate can make a real decision after each answer.
package syllabus

import (
	"strings"
)

// Direction values are stable and used in events/logs and API responses.
const (
	DirectionGo    = "go"
	DirectionMySQL = "mysql"
	DirectionRedis = "redis"
)

// Item is one outline knowledge point.
type Item struct {
	ID             string   `json:"id"`
	Topic          string   `json:"topic"`
	Objective      string   `json:"objective"`
	ExpectedPoints []string `json:"expected_points"`
	Difficulty     string   `json:"difficulty"`
}

// Outline is an ordered knowledge-point list for one or more directions.
type Outline struct {
	Direction string `json:"direction"`
	Items     []Item `json:"items"`
}

// Directions returns the supported built-in directions in canonical order.
func Directions() []string {
	return []string{DirectionGo, DirectionMySQL, DirectionRedis}
}

var builtins = map[string][]Item{
	DirectionGo: {
		{
			ID:         "go-concurrency",
			Topic:      "Go 并发模型",
			Objective:  "说明 goroutine、channel 与 context 在真实服务中的配合方式",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"goroutine 调度与 G-M-P 模型",
				"channel 阻塞与缓冲区设计",
				"context 取消与超时传递",
			},
		},
		{
			ID:         "go-arch-layering",
			Topic:      "服务分层与依赖边界",
			Objective:  "说明这一层为什么存在、删掉会怎样，以及依赖方向如何守住",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"分层职责与依赖方向",
				"领域层不依赖基础设施",
				"端口与适配器隔离",
			},
		},
		{
			ID:         "go-memory",
			Topic:      "内存管理与 GC",
			Objective:  "解释内存分配、GC 原理以及一次真实的 GC 问题排查",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"逃逸分析",
				"三色标记与写屏障",
				"GC 调优与内存泄漏排查",
			},
		},
		{
			ID:         "go-interface",
			Topic:      "接口、反射与泛型",
			Objective:  "说明接口动态派发和泛型约束的代价与适用场景",
			Difficulty: "senior",
			ExpectedPoints: []string{
				"接口动态派发与 itab",
				"类型断言与空接口陷阱",
				"泛型类型约束",
			},
		},
		{
			ID:         "go-error",
			Topic:      "错误处理与稳定性",
			Objective:  "说明错误链路、panic 边界以及超时重试下的幂等保障",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"错误包装与 errors.Is/As",
				"panic/recover 的边界",
				"超时、重试与幂等",
			},
		},
		{
			ID:         "go-arch-idempotency",
			Topic:      "幂等与状态机设计",
			Objective:  "说明接口重复调用与状态迁移如何保证只生效一次",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"幂等键与去重存储",
				"状态机合法迁移",
				"重复请求与重试语义",
			},
		},
		{
			ID:         "go-network",
			Topic:      "网络编程与连接治理",
			Objective:  "说明 HTTP 超时链路、连接复用与优雅停机的实现",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"net/http 超时链路",
				"连接池与复用",
				"优雅停机",
			},
		},
		{
			ID:         "go-arch-lock",
			Topic:      "并发与锁的工程取舍",
			Objective:  "说明共享状态如何加锁、锁粒度如何取舍、失锁如何降级",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"锁粒度与争用成本",
				"死锁预防与超时",
				"进程锁与分布式锁的边界",
			},
		},
		{
			ID:         "go-db",
			Topic:      "数据库访问与事务",
			Objective:  "说明连接池、事务边界以及一次慢查询定位",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"database/sql 连接池",
				"事务边界与回滚",
				"SQL 慢查询定位",
			},
		},
		{
			ID:         "go-arch-observability",
			Topic:      "可观测性设计取舍",
			Objective:  "说明指标、日志与追踪为什么这样分层，删掉一类信号会怎样",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"指标、日志与追踪的分工",
				"采样与成本控制",
				"从告警到定位的路径",
			},
		},
		{
			ID:         "go-observability",
			Topic:      "可观测性与故障排查",
			Objective:  "复述一次线上故障从发现到定位再到修复的完整过程",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"pprof 与 trace",
				"结构化日志与指标",
				"线上故障定位路径",
			},
		},
		{
			ID:         "go-arch-degradation",
			Topic:      "降级与失败策略",
			Objective:  "说明依赖失败时如何降级、哪些失败必须显式暴露",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"超时、重试与熔断预算",
				"降级路径与数据一致性",
				"失败可观测与回滚",
			},
		},
	},
	DirectionMySQL: {
		{
			ID:         "mysql-index",
			Topic:      "索引与执行计划",
			Objective:  "说明索引结构、回表代价以及如何读懂执行计划",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"B+ 树索引结构",
				"最左前缀与回表",
				"覆盖索引与执行计划判断",
			},
		},
		{
			ID:         "mysql-mvcc",
			Topic:      "事务与 MVCC",
			Objective:  "解释隔离级别下的可见性规则和一次并发场景验证",
			Difficulty: "senior",
			ExpectedPoints: []string{
				"隔离级别与可见性",
				"Read View 与 undo log",
				"幻读与间隙锁",
			},
		},
		{
			ID:         "mysql-lock",
			Topic:      "锁与死锁",
			Objective:  "说明行锁、死锁产生条件以及线上死锁排查方式",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"行锁与意向锁",
				"死锁检测与排查",
				"锁等待超时处理",
			},
		},
		{
			ID:         "mysql-slow",
			Topic:      "慢查询优化",
			Objective:  "给出一次从慢日志到索引调整的完整优化过程",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"慢日志与 EXPLAIN",
				"统计信息与索引选择",
				"分页与排序优化",
			},
		},
		{
			ID:         "mysql-replica",
			Topic:      "复制与高可用",
			Objective:  "说明主从复制原理、延迟影响与切换时的数据一致性",
			Difficulty: "senior",
			ExpectedPoints: []string{
				"主从复制原理",
				"复制延迟与一致性读",
				"故障切换与数据校验",
			},
		},
		{
			ID:         "mysql-shard",
			Topic:      "分库分表与数据迁移",
			Objective:  "说明分片键选择、跨片查询限制以及不停机迁移方案",
			Difficulty: "senior",
			ExpectedPoints: []string{
				"分片键选择",
				"跨分片查询与事务",
				"双写迁移与校验",
			},
		},
	},
	DirectionRedis: {
		{
			ID:         "redis-structure",
			Topic:      "数据结构与内存编码",
			Objective:  "说明常用类型的底层编码以及大 key 的治理方式",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"ziplist/listpack 编码转换",
				"跳表与有序集合",
				"大 key 与内存碎片",
			},
		},
		{
			ID:         "redis-persist",
			Topic:      "持久化与恢复",
			Objective:  "对比 RDB 与 AOF，并说明恢复时间和数据安全的取舍",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"RDB 与 AOF 差异",
				"fsync 策略与数据安全",
				"恢复时间与容量规划",
			},
		},
		{
			ID:         "redis-cache",
			Topic:      "缓存模式与一致性",
			Objective:  "说明穿透、击穿、雪崩的处理以及缓存与数据库一致性",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"缓存穿透、击穿、雪崩",
				"热 key 与大 key 治理",
				"缓存与数据库一致性",
			},
		},
		{
			ID:         "redis-lock",
			Topic:      "分布式锁",
			Objective:  "说明锁的获取、续租、误删防护以及失效后的降级",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"SET NX PX 与唯一值",
				"续租与误删防护",
				"锁失效与降级方案",
			},
		},
		{
			ID:         "redis-ha",
			Topic:      "高可用与集群",
			Objective:  "说明主从、哨兵、Cluster 的故障转移与一致性取舍",
			Difficulty: "senior",
			ExpectedPoints: []string{
				"主从与哨兵",
				"Cluster 槽位与迁移",
				"脑裂与一致性取舍",
			},
		},
		{
			ID:         "redis-limit",
			Topic:      "限流与原子脚本",
			Objective:  "说明如何用 Lua 实现原子限流以及集群下的限制",
			Difficulty: "mid",
			ExpectedPoints: []string{
				"Lua 脚本原子性",
				"滑动窗口与令牌桶",
				"集群下脚本限制",
			},
		},
	},
}

// Builtin returns a copy of one built-in outline.
func Builtin(direction string) (Outline, bool) {
	direction = strings.ToLower(strings.TrimSpace(direction))
	items, ok := builtins[direction]
	if !ok {
		return Outline{}, false
	}
	return Outline{Direction: direction, Items: append([]Item(nil), items...)}, true
}

var directionKeywords = map[string][]string{
	DirectionGo:    {"go", "golang", "后端", "服务端", "backend"},
	DirectionMySQL: {"mysql", "mariadb", "数据库", "dba", "sql"},
	DirectionRedis: {"redis", "缓存", "cache"},
}

// Detect returns every direction whose keywords appear in text, in canonical
// order. An empty result means the caller should fall back to the Go outline.
func Detect(text string) []string {
	lower := strings.ToLower(text)
	var out []string
	for _, direction := range Directions() {
		for _, keyword := range directionKeywords[direction] {
			if strings.Contains(lower, keyword) {
				out = append(out, direction)
				break
			}
		}
	}
	return out
}

// Plan merges matched directions into one ordered outline, removes duplicate
// topics and keeps at most limit items. Items above the candidate difficulty
// are placed after matching ones so a junior session stays approachable while
// still filling the outline.
func Plan(text, difficulty string, limit int) Outline {
	if limit <= 0 {
		limit = 8
	}
	matched := Detect(text)
	if len(matched) == 0 {
		matched = []string{DirectionGo}
	}

	rank := difficultyRank(difficulty)
	var primary, secondary []Item
	seen := map[string]struct{}{}
	for _, direction := range matched {
		items, ok := builtins[direction]
		if !ok {
			continue
		}
		for _, item := range items {
			if _, dup := seen[item.Topic]; dup {
				continue
			}
			seen[item.Topic] = struct{}{}
			if difficultyRank(item.Difficulty) <= rank {
				primary = append(primary, item)
			} else {
				secondary = append(secondary, item)
			}
		}
	}

	items := append(primary, secondary...)
	if len(items) > limit {
		items = items[:limit]
	}
	return Outline{
		Direction: strings.Join(matched, "+"),
		Items:     append([]Item(nil), items...),
	}
}

func difficultyRank(difficulty string) int {
	switch strings.ToLower(strings.TrimSpace(difficulty)) {
	case "junior":
		return 1
	case "senior":
		return 3
	default:
		return 2
	}
}
