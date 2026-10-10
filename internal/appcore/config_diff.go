// config_diff.go 判断「面板这次保存的改动，是否需要重启进程才生效」。
//
// 背景：restartRequiredFields 是**按配置语义**列出的全部装配期字段
// （listen/auth_dir/state_file/upstream.*/upstash/session_sticky TTL），不论用户这次
// 改了哪个。服务端部署这样提示是对的（用户自己重启容器，多提示无害），但桌面版会变成
// 「随便改个东西都告诉你需要重启」——而桌面版其实能自己重启内核。
//
// 所以这里回答一个更精确的问题：**合并后的新配置，与当前正在运行的配置相比，
// 装配期部分有无变化？** 有变化才需要重启；没变化就如实说「已立即生效」。
//
// 比较对象的选择很重要：比的是「合并后的配置」而不是「面板提交的原始 JSON」。
// 面板只管它表单里的键，磁盘上还有它不提交的键（如桌面模式写下的绝对路径
// auth_dir）。saveConfig 先深合并再解析，得到的才是真正会生效的配置——
// 拿原始提交去比，会在桌面模式下把「没提交路径」误判成「路径变了」。
//
// 实现是「整块结构体比较 − 已热生效字段 − 推导字段」：
//
//	用反射把已经热生效的字段与全部推导字段（json:"-"）清零，
//	剩下的相等 ⇒ 无需重启。
//
// 用整块比较而不是逐字段列举：新增配置字段时不需要记得回来改这里。漏改的后果是
// 「改了新字段但没提示重启」——一个会静默骗用户的错误。
package appcore

import (
	"reflect"
	"slices"
	"strings"
)

// hotAppliedFields 面板保存时**立即生效**、不需要重启的顶层字段名（按 JSON 名）。
// 与 config_save.go 的热应用清单一一对应，两处必须同步。
var hotAppliedFields = []string{
	"api_key",     // livecfg 快照，下一个请求即生效
	"client_keys", // livecfg 快照（展开成 Creds），新增/停用/删除下一个请求即生效
	"cooldown",    // livecfg 快照（soft_rate / soft_rate_max）
	"features", // up.SanitizeFingerprints 直接赋值
	"pool",     // pool 的一批 Set* 方法
	"schedule", // Scheduler.Reconfigure / SetBalanceInterval
}

// assemblyFieldsDiffer 判断 current（正在运行）与 next（本次保存后生效）在装配期
// 字段上是否存在差异。true = 需要重启进程/内核。
func assemblyFieldsDiffer(current, next *Config) bool {
	if current == nil || next == nil {
		return true // 信息不全时保守要求重启
	}
	return !reflect.DeepEqual(stripHotAndDerived(current), stripHotAndDerived(next))
}

// stripHotAndDerived 返回配置的副本，其中热生效字段与全部推导字段（json:"-"）被清零。
//
// 清零推导字段是必须的：它们由其他字段推导（Duration 类由时长字符串解析、PromptText
// 由 prompt.mode/file 加载），保留会带来假差异。json:"-" 的标记正好是这个语义，
// 因此直接用 tag 判定，而不是维护第二份字段清单。
func stripHotAndDerived(c *Config) *Config {
	cp := *c
	v := reflect.ValueOf(&cp).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" || slices.Contains(hotAppliedFields, name) {
			v.Field(i).SetZero()
		}
	}
	return &cp
}
