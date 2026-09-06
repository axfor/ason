package llm

// 引擎来自 github.com/axfor/ason（协议无关的流式 JSON 转换：扫描器、动作、惰性写出器、严格校验）。
// 这里用别名把它的 API 原样暴露在本包，协议文件（proto_*.go、hook_tools.go）不感知包边界。

import "github.com/axfor/ason"

type (
	Transformer  = ason.Transformer
	Action       = ason.Action
	ValueKind    = ason.ValueKind
	Protocol     = ason.Protocol
	BaseProtocol = ason.BaseProtocol
	Writer       = ason.Writer
	DeferredKV   = ason.DeferredKV
)

const (
	KindString = ason.KindString
	KindObject = ason.KindObject
	KindArray  = ason.KindArray
	KindNull   = ason.KindNull
	KindBool   = ason.KindBool
	KindNumber = ason.KindNumber

	// CommitBytes 是提交点窗口：扫描这么多输入字节之前不下发任何输出。
	CommitBytes = ason.CommitBytes
)

var (
	NewTransformer = ason.NewTransformer
	Pass           = ason.Pass
	Skip           = ason.Skip
	Capture        = ason.Capture
	Observe        = ason.Observe
	Defer          = ason.Defer
	Enter          = ason.Enter
	Probe          = ason.Probe
	Prefix         = ason.Prefix
	Bail           = ason.Bail
)

// 协议实现里用到的通用辅助
func appendJSONString(dst []byte, s string) []byte { return ason.AppendJSONString(dst, s) }
func jsonUnquote(raw []byte) (string, bool)        { return ason.JSONUnquote(raw) }
func unescapePrefix(b []byte) ([]byte, []int)      { return ason.UnescapePrefix(b) }
func isIntLiteral(s []byte) bool                   { return ason.IsIntLiteral(s) }
func isNumLiteral(s []byte) bool                   { return ason.IsNumLiteral(s) }
func isZeroNum(s []byte) bool                      { return ason.IsZeroNum(s) }
