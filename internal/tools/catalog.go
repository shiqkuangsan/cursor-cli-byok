package tools

import (
	"encoding/json"
	"errors"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

var builtinTools = []provider.Tool{
	tool("Read", "Read a UTF-8 file", `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`),
	tool("Write", "Write a complete file", `{"type":"object","properties":{"path":{"type":"string"},"contents":{"type":"string"}},"required":["path","contents"],"additionalProperties":false}`),
	tool("Edit", "Replace text in a file", `{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old_string","new_string"],"additionalProperties":false}`),
	tool("Delete", "Delete a file", `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
	tool("List", "List a directory", `{"type":"object","properties":{"path":{"type":"string"},"depth":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`),
	tool("Glob", "Find files matching a glob", `{"type":"object","properties":{"glob_pattern":{"type":"string"},"target_directory":{"type":"string"}},"required":["glob_pattern"],"additionalProperties":false}`),
	tool("Grep", "Search file contents", `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"},"output_mode":{"type":"string","enum":["content","files_with_matches","count"]}},"required":["pattern"],"additionalProperties":false}`),
	tool("Shell", "Run a shell command", `{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string"},"working_directory":{"type":"string"},"block_until_ms":{"type":"number","minimum":0}},"required":["command"],"additionalProperties":false}`),
	tool("CallMcpTool", "Call a configured MCP tool", `{"type":"object","properties":{"server":{"type":"string"},"tool_name":{"type":"string"},"arguments":{"type":"object"}},"required":["server","tool_name","arguments"],"additionalProperties":false}`),
}

func BuiltinCatalog() []provider.Tool {
	return cloneTools(builtinTools)
}

func Select(names ...string) ([]provider.Tool, error) {
	byName := make(map[string]provider.Tool, len(builtinTools))
	for _, item := range builtinTools {
		byName[item.Name] = item
	}
	selected := make([]provider.Tool, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		item, ok := byName[name]
		if !ok {
			return nil, errors.New("select tools: unsupported tool name")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, errors.New("select tools: duplicate tool name")
		}
		seen[name] = struct{}{}
		selected = append(selected, cloneTool(item))
	}
	return selected, nil
}

func tool(name, description, parameters string) provider.Tool {
	return provider.Tool{Name: name, Description: description, Parameters: json.RawMessage(parameters)}
}

func cloneTools(items []provider.Tool) []provider.Tool {
	result := make([]provider.Tool, len(items))
	for index, item := range items {
		result[index] = cloneTool(item)
	}
	return result
}

func cloneTool(item provider.Tool) provider.Tool {
	cloned := item
	cloned.Parameters = append(json.RawMessage(nil), item.Parameters...)
	return cloned
}
