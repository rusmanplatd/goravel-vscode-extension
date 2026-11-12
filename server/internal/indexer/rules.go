package indexer

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
)

var builtinRules = []struct {
	name, doc string
}{
	{"required", "Field must be present and not empty."},
	{"required_if", "required_if:field,value,... — Required if another field equals any value."},
	{"required_unless", "required_unless:field,value,... — Required unless another field equals any value."},
	{"required_with", "required_with:foo,bar,... — Required if any of the other specified fields are present."},
	{"required_with_all", "required_with_all:foo,bar,... — Required if all of the other specified fields are present."},
	{"required_without", "required_without:foo,bar,... — Required if any of the other specified fields are not present."},
	{"required_without_all", "required_without_all:foo,bar,... — Required if all of the other specified fields are not present."},
	{"nullable", "Field may be null."},
	{"sometimes", "Validate field only when present."},
	{"string", "Field must be a string."},
	{"integer", "Field must be an integer."},
	{"int", "Field must be an integer."},
	{"uint", "Field must be an unsigned integer."},
	{"numeric", "Field must be numeric."},
	{"number", "Field must be a number string."},
	{"float", "Field must be a floating-point number."},
	{"decimal", "Field must be a decimal number."},
	{"bool", "Field must be a boolean."},
	{"boolean", "Field must be able to be cast as a boolean."},
	{"array", "Field must be an array or slice."},
	{"slice", "Field must be a slice."},
	{"map", "Field must be a map."},
	{"email", "Field must be formatted as an e-mail address."},
	{"uuid", "Field must be a valid UUID."},
	{"uuid3", "Field must be a valid UUID v3."},
	{"uuid4", "Field must be a valid UUID v4."},
	{"uuid5", "Field must be a valid UUID v5."},
	{"ulid", "Field must be a valid ULID."},
	{"url", "Field must be a valid URL."},
	{"full_url", "Field must be a full URL starting with http:// or https://."},
	{"ip", "Field must be an IP address."},
	{"ipv4", "Field must be an IPv4 address."},
	{"ipv6", "Field must be an IPv6 address."},
	{"mac", "Field must be a MAC address."},
	{"alpha", "Field must be entirely alphabetic characters."},
	{"alpha_num", "Field must be entirely alpha-numeric characters."},
	{"alphanum", "Field must be entirely alpha-numeric characters."},
	{"alpha_dash", "Field may have alpha-numeric characters, as well as dashes and underscores."},
	{"alpha_space", "Field may have alphabetic characters and spaces."},
	{"ascii", "Field must be entirely 7-bit ASCII characters."},
	{"min", "min:value — Minimum value."},
	{"max", "max:value — Maximum value."},
	{"min_len", "min_len:value — Minimum string/slice length."},
	{"max_len", "max_len:value — Maximum string/slice length."},
	{"between", "between:min,max — Value or length must be between min and max."},
	{"len", "len:value — Exact length of string/slice."},
	{"size", "size:value — Size matching the given value."},
	{"gt", "gt:field — Must be greater than the given field or value."},
	{"gte", "gte:field — Must be greater than or equal to the given field or value."},
	{"lt", "lt:field — Must be less than the given field or value."},
	{"lte", "lte:field — Must be less than or equal to the given field or value."},
	{"eq", "eq:value — Must be equal to the given value."},
	{"ne", "ne:value — Must not be equal to the given value."},
	{"in", "in:foo,bar,... — Value must be included in the given list."},
	{"not_in", "not_in:foo,bar,... — Value must not be included in the given list."},
	{"regex", "regex:pattern — Value must match the given regular expression."},
	{"not_regex", "not_regex:pattern — Value must not match the given regular expression."},
	{"same", "same:field — Value must match the given field."},
	{"different", "different:field — Value must have a different value than field."},
	{"starts_with", "starts_with:foo,bar,... — Field must start with one of the given values."},
	{"ends_with", "ends_with:foo,bar,... — Field must end with one of the given values."},
	{"date", "Field must be a valid date."},
	{"date_format", "date_format:format — Field must match the given date format."},
	{"before", "before:date — Field must be a value preceding the given date."},
	{"before_or_equal", "before_or_equal:date — Field must be a value preceding or equal to the given date."},
	{"after", "after:date — Field must be a value after the given date."},
	{"after_or_equal", "after_or_equal:date — Field must be a value after or equal to the given date."},
	{"json", "Field must be a valid JSON string."},
	{"file", "Field must be a successfully uploaded file."},
	{"image", "Field must be an image (jpg, jpeg, png, bmp, gif, svg, or webp)."},
	{"mimes", "mimes:foo,bar,... — File must have a MIME type matching one of the listed extensions."},
	{"mimetypes", "mimetypes:text/plain,... — File must match one of the given MIME types."},
	{"exists", "exists:table,column — Field must exist in the given database table."},
	{"unique", "unique:table,column — Field must be unique in the given database table."},
	{"hex_color", "Field must be a valid hex color code."},
	{"rgb_color", "Field must be a valid RGB color."},
	{"timezone", "Field must be a valid timezone identifier."},
	{"base64", "Field must be a valid base64 string."},
	{"data_uri", "Field must be a valid data URI."},
	{"latitude", "Field must be a valid latitude (-90 to 90)."},
	{"longitude", "Field must be a valid longitude (-180 to 180)."},
}

func seedBuiltinRules(c *Catalog) {
	for _, r := range builtinRules {
		c.Rules[r.name] = &RuleItem{Name: r.name, Custom: false, Doc: r.doc}
	}
}

func indexRules(c *Catalog, root string) {
	files := walkGoFiles(filepath.Join(root, "app", "rules"))
	for _, pf := range files {
		sigs := map[string]struct {
			sig string
			loc Location
		}{}
		msgs := map[string]string{}
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil {
				continue
			}
			recv := recvName(fd.Recv)
			switch fd.Name.Name {
			case "Signature":
				if s := returnString(fd.Body); s != "" {
					sigs[recv] = struct {
						sig string
						loc Location
					}{s, loc(pf, fd.Name.Pos())}
				}
			case "Message":
				if m := returnString(fd.Body); m != "" {
					msgs[recv] = m
				}
			}
		}
		for recv, info := range sigs {
			c.Rules[info.sig] = &RuleItem{
				Name:    info.sig,
				Custom:  true,
				Message: msgs[recv],
				Doc:     "Custom validation rule `" + recv + "` (`app/rules`)",
				Loc:     info.loc,
			}
		}
	}
}

func returnString(body *ast.BlockStmt) string {
	var out string
	ast.Inspect(body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		if bl, ok := ret.Results[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
			if s, err := strconv.Unquote(bl.Value); err == nil {
				out = s
				return false
			}
		}
		return true
	})
	return out
}
