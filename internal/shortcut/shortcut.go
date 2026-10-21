// Package shortcut builds the iOS shortcut that drives the server's
// /v1/flow API. The shortcut only logs in, uploads a screenshot and then loops
// over the steps the server returns; see internal/server/flow.go.
package shortcut

import (
	"fmt"
	"maps"
	"strings"
	"unicode/utf16"
)

// Options fill in the import questions' default answers.
type Options struct {
	Server string
	Invite string
}

// TokenFile holds the service token, in the Shortcuts folder of iCloud Drive.
const TokenFile = "ledger-snap-token.txt"

// Rounds caps the dialogs in one run; a run normally needs two or three.
const Rounds = 30

// Build returns the unsigned shortcut as an XML property list.
func Build(o Options) ([]byte, error) {
	b := &builder{}

	// The import questions fill in these two text actions, found by index.
	serverIndex := len(b.actions)
	serverText := b.output("gettext", "Text", dict{"WFTextActionText": o.Server})
	server := b.set("服务器", b.output("text.replace", "Updated Text", dict{
		"WFInput":                        text(serverText),
		"WFReplaceTextFind":              `\s+|/+$`,
		"WFReplaceTextReplace":           "",
		"WFReplaceTextRegularExpression": true,
	}))
	inviteIndex := len(b.actions)
	invite := b.set("邀请码", b.output("gettext", "Text", dict{"WFTextActionText": o.Invite}))

	token := b.set("令牌", b.getTokenFile())
	b.ifThen(token, hasNoValue, func() {
		account := b.ask("钱迹账号（手机号或邮箱）", dict{})
		password := b.ask("钱迹密码", dict{})
		md5 := b.output("hash", "Hash", dict{"WFInput": attach(password), "WFHashType": "MD5"})
		login := b.post(text(server, "/v1/flow/login"), nil, dict{
			"WFHTTPBodyType": "JSON",
			"WFJSONValues":   fields("account", text(account), "password_md5", text(md5), "invite_code", text(invite)),
		})
		newToken := b.value(login, "token")
		b.ifThen(newToken, hasNoValue, func() {
			b.alert("登录失败", b.value(login, "message"))
			b.exit()
		}, nil)
		b.action("documentpicker.save", dict{
			"WFInput":               attach(newToken),
			"WFFileDestinationPath": TokenFile,
			"WFSaveFileOverwrite":   true,
			"WFAskWhereToSave":      false,
		})
		b.set("令牌", newToken)
	}, nil)

	auth := fields("Authorization", text("Bearer ", token))
	// Wait for the shortcut's running animation to go before the screenshot.
	b.action("delay", dict{"WFDelayTime": 1})
	shot := b.output("takescreenshot", "Screenshot", dict{})
	jpeg := b.output("image.convert", "Converted Image", dict{
		"WFInput":                   attach(shot),
		"WFImageFormat":             "JPEG",
		"WFImageCompressionQuality": 0.8,
		"WFImagePreserveMetadata":   false,
	})
	reply := b.set("回应", b.post(text(server, "/v1/flow/recognize"), auth, dict{
		"WFHTTPBodyType":    "File",
		"WFRequestVariable": attach(jpeg),
	}))

	b.repeat(Rounds, func() {
		action := b.value(reply, "action")
		b.ifThen(action, hasNoValue, func() {
			b.alert("截图记账", "服务器没有正常回应，请检查快捷指令里的服务器地址")
			b.exit()
		}, nil)
		b.ifThen(action, equals("done"), func() {
			b.action("notification", dict{
				"WFNotificationActionTitle": "截图记账",
				"WFNotificationActionBody":  text(b.value(reply, "message")),
				"WFNotificationActionSound": false,
			})
			b.exit()
		}, nil)
		b.ifThen(action, equals("relogin"), func() {
			// Delete Files fails on empty input, so only delete a file that is there.
			saved := b.getTokenFile()
			b.ifThen(saved, hasValue, func() {
				b.action("file.delete", dict{
					"WFInput":                     attach(saved),
					"WFDeleteFileConfirmDeletion": false,
					"WFDeleteImmediatelyDelete":   true,
				})
			}, nil)
			b.alert("需要重新登录", b.value(reply, "message"))
			b.exit()
		}, nil)
		prompt := b.set("提示", b.value(reply, "prompt"))
		next := b.set("下一步", b.value(reply, "next"))
		b.ifThen(action, equals("text"), func() {
			b.set("回答", b.ask(prompt, dict{
				"WFAskActionDefaultAnswer": text(b.value(reply, "default")),
				"WFAllowsMultilineText":    false,
			}))
		}, func() {
			b.ifThen(action, equals("number"), func() {
				b.set("回答", b.ask(prompt, dict{
					"WFInputType":                      "Number",
					"WFAskActionAllowsDecimalNumbers":  true,
					"WFAskActionAllowsNegativeNumbers": false,
				}))
			}, func() {
				b.set("回答", b.output("choosefromlist", "Chosen Item", dict{
					"WFInput":                      attach(b.value(reply, "options")),
					"WFChooseFromListActionPrompt": text(prompt),
				}))
			})
		})
		b.set("回应", b.post(text(server, next), auth, dict{
			"WFHTTPBodyType": "JSON",
			"WFJSONValues":   fields("value", text(variable("回答"))),
		}))
	})

	return encodePlist(dict{
		"WFWorkflowActions":                    b.actions,
		"WFWorkflowClientVersion":              "5037.0.17",
		"WFWorkflowMinimumClientVersion":       900,
		"WFWorkflowMinimumClientVersionString": "900",
		"WFWorkflowIcon": dict{
			"WFWorkflowIconStartColor":  4292093695, // green
			"WFWorkflowIconGlyphNumber": 59514,      // yen sign
		},
		"WFWorkflowImportQuestions": []any{
			question(serverIndex, "服务器地址，例如 https://snap.example.com", o.Server),
			question(inviteIndex, "邀请码", o.Invite),
		},
		"WFWorkflowInputContentItemClasses":   []any{},
		"WFWorkflowOutputContentItemClasses":  []any{},
		"WFWorkflowTypes":                     []any{},
		"WFQuickActionSurfaces":               []any{},
		"WFWorkflowHasShortcutInputVariables": false,
		"WFWorkflowHasOutputFallback":         false,
	})
}

func question(actionIndex int, prompt, answer string) dict {
	return dict{"ActionIndex": actionIndex, "Category": "Parameter", "ParameterKey": "WFTextActionText", "Text": prompt, "DefaultValue": answer}
}

// builder appends actions in order. Control flow takes closures, so the Go
// code nests the same way the shortcut does.
type builder struct {
	actions []any
	ids     int
}

// ref points at a value: a named variable or an earlier action's output.
type ref dict

func variable(name string) ref { return ref{"Type": "Variable", "VariableName": name} }

// uuid numbers the actions deterministically, so rebuilding gives the same file.
func (b *builder) uuid() string {
	b.ids++
	return fmt.Sprintf("4C534E50-0000-4000-8000-%012X", b.ids)
}

func (b *builder) action(id string, params dict) {
	b.actions = append(b.actions, dict{"WFWorkflowActionIdentifier": "is.workflow.actions." + id, "WFWorkflowActionParameters": params})
}

// output adds an action whose result later actions refer to.
func (b *builder) output(id, name string, params dict) ref {
	uuid := b.uuid()
	params["UUID"] = uuid
	b.action(id, params)
	return ref{"Type": "ActionOutput", "OutputUUID": uuid, "OutputName": name}
}

func (b *builder) set(name string, value ref) ref {
	b.action("setvariable", dict{"WFVariableName": name, "WFInput": attach(value)})
	return variable(name)
}

func (b *builder) value(from ref, key string) ref {
	return b.output("getvalueforkey", "Dictionary Value", dict{"WFInput": attach(from), "WFDictionaryKey": key, "WFGetDictionaryValueType": "Value"})
}

func (b *builder) getTokenFile() ref {
	return b.output("documentpicker.open", "File", dict{"WFGetFilePath": TokenFile, "WFFileErrorIfNotFound": false, "WFShowFilePicker": false})
}

// ask shows Ask for Input; prompt is a string or a ref.
func (b *builder) ask(prompt any, params dict) ref {
	params["WFAskActionPrompt"] = text(prompt)
	if params["WFInputType"] == nil {
		params["WFInputType"] = "Text"
	}
	return b.output("ask", "Provided Input", params)
}

// post sends Get Contents of URL as a POST; body holds the body parameters.
func (b *builder) post(url, headers, body dict) ref {
	params := dict{"WFURL": url, "WFHTTPMethod": "POST"}
	if headers != nil {
		params["ShowHeaders"] = true
		params["WFHTTPHeaders"] = headers
	}
	for k, v := range body {
		params[k] = v
	}
	return b.output("downloadurl", "Contents of URL", params)
}

// alert shows a dialog with only an OK button; message is a string or a ref.
func (b *builder) alert(title string, message any) {
	b.action("alert", dict{"WFAlertActionTitle": title, "WFAlertActionMessage": text(message), "WFAlertActionCancelButtonShown": false})
}

func (b *builder) exit() { b.action("exit", dict{}) }

type condition struct {
	code  int
	value string
}

var (
	hasValue   = condition{code: 100}
	hasNoValue = condition{code: 101}
)

func equals(s string) condition { return condition{code: 4, value: s} }

func (b *builder) ifThen(input ref, c condition, then, otherwise func()) {
	group := b.uuid()
	start := dict{"GroupingIdentifier": group, "WFControlFlowMode": 0, "WFCondition": c.code}
	if c.value != "" {
		// Shortcuts ignores the text to compare with unless the input is
		// known to be text, which a dictionary value is not.
		input = asText(input)
		start["WFConditionalActionString"] = c.value
	}
	start["WFInput"] = dict{"Type": "Variable", "Variable": attach(input)}
	b.action("conditional", start)
	then()
	if otherwise != nil {
		b.action("conditional", dict{"GroupingIdentifier": group, "WFControlFlowMode": 1})
		otherwise()
	}
	b.action("conditional", dict{"GroupingIdentifier": group, "WFControlFlowMode": 2, "UUID": b.uuid()})
}

func (b *builder) repeat(count int, body func()) {
	group := b.uuid()
	b.action("repeat.count", dict{"GroupingIdentifier": group, "WFControlFlowMode": 0, "WFRepeatCount": count})
	body()
	b.action("repeat.count", dict{"GroupingIdentifier": group, "WFControlFlowMode": 2, "UUID": b.uuid()})
}

// asText returns r coerced to text, as picking "Text" on a variable does.
func asText(r ref) ref {
	t := maps.Clone(r)
	t["Aggrandizements"] = []any{dict{"Type": "WFCoercionVariableAggrandizement", "CoercionItemClass": "WFStringContentItem"}}
	return t
}

func attach(r ref) dict {
	return dict{"Value": dict(r), "WFSerializationType": "WFTextTokenAttachment"}
}

// text builds a text parameter from strings and refs. Each ref becomes an
// object replacement character, and its range counts UTF-16 code units.
func text(parts ...any) dict {
	var s strings.Builder
	ranges := dict{}
	pos := 0
	for _, p := range parts {
		switch p := p.(type) {
		case string:
			s.WriteString(p)
			pos += len(utf16.Encode([]rune(p)))
		case ref:
			ranges[fmt.Sprintf("{%d, 1}", pos)] = dict(p)
			s.WriteRune('￼')
			pos++
		default:
			panic(fmt.Sprintf("text: unsupported part %T", p))
		}
	}
	value := dict{"string": s.String()}
	if len(ranges) > 0 {
		value["attachmentsByRange"] = ranges
	}
	return dict{"Value": value, "WFSerializationType": "WFTextTokenString"}
}

// fields builds a dictionary parameter (JSON body or headers) from key,
// value pairs, where each value is a text parameter.
func fields(pairs ...any) dict {
	var items []any
	for i := 0; i+1 < len(pairs); i += 2 {
		items = append(items, dict{"WFItemType": 0, "WFKey": text(pairs[i].(string)), "WFValue": pairs[i+1]})
	}
	return dict{"Value": dict{"WFDictionaryFieldValueItems": items}, "WFSerializationType": "WFDictionaryFieldValue"}
}
