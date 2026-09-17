package ollama

import (
	"github.com/emaharmony/prizm/internal/tool"
)

// ConvertToolsToOllama converts Prizm tool definitions to Ollama's function schema format.
//
// Security: Only public metadata (name, description, parameter schemas) is exposed.
// No workspace paths, implementation details, or file contents are included.
func ConvertToolsToOllama(toolInfos []tool.ToolInfo) []ollamaFunction {
	functions := make([]ollamaFunction, 0, len(toolInfos))
	for _, ti := range toolInfos {
		params := map[string]any{
			"type":       "object",
			"properties": make(map[string]any),
		}
		required := make([]string, 0)

		for pname, spec := range ti.Schema.Input {
			props := map[string]any{
				"type":        spec.Type,
				"description": spec.Description,
			}
			params["properties"].(map[string]any)[pname] = props
			if spec.Required {
				required = append(required, pname)
			}
		}

		if len(required) > 0 {
			params["required"] = required
		}

		fn := ollamaFunction{
			Type: "function",
		}
		fn.Function.Name = ti.Name
		fn.Function.Description = ti.Description
		fn.Function.Parameters = params
		functions = append(functions, fn)
	}
	return functions
}
