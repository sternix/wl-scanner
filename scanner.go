package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type (
	Protocol struct {
		XMLName    xml.Name    `xml:"protocol"`
		Name       string      `xml:"name,attr"`
		Copyright  string      `xml:"copyright"`
		Interfaces []Interface `xml:"interface"`
	}

	Description struct {
		XMLName     xml.Name `xml:"description"`
		Summary     string   `xml:"summary,attr"`
		Description string   `xml:"description"`
	}

	Interface struct {
		XMLName     xml.Name    `xml:"interface"`
		Name        string      `xml:"name,attr"`
		Version     int         `xml:"version,attr"`
		Since       int         `xml:"since,attr"` // maybe in future versions
		Description Description `xml:"description"`
		Requests    []Request   `xml:"request"`
		Events      []Event     `xml:"event"`
		Enums       []Enum      `xml:"enum"`
	}

	Request struct {
		XMLName     xml.Name    `xml:"request"`
		Name        string      `xml:"name,attr"`
		Type        string      `xml:"type,attr"`
		Since       int         `xml:"since,attr"`
		Description Description `xml:"description"`
		Args        []Arg       `xml:"arg"`
	}

	Arg struct {
		XMLName   xml.Name `xml:"arg"`
		Name      string   `xml:"name,attr"`
		Type      string   `xml:"type,attr"`
		Interface string   `xml:"interface,attr"`
		Enum      string   `xml:"enum,attr"`
		AllowNull bool     `xml:"allow-null,attr"`
		Summary   string   `xml:"summary,attr"`
	}

	Event struct {
		XMLName     xml.Name    `xml:"event"`
		Name        string      `xml:"name,attr"`
		Since       int         `xml:"since,attr"`
		Description Description `xml:"description"`
		Args        []Arg       `xml:"arg"`
	}

	Enum struct {
		XMLName     xml.Name    `xml:"enum"`
		Name        string      `xml:"name,attr"`
		BitField    bool        `xml:"bitfield,attr"`
		Description Description `xml:"description"`
		Entries     []Entry     `xml:"entry"`
	}

	Entry struct {
		XMLName xml.Name `xml:"entry"`
		Name    string   `xml:"name,attr"`
		Value   string   `xml:"value,attr"`
		Summary string   `xml:"summary,attr"`
	}
)

var (
	wlTypes map[string]string = map[string]string{
		"int":    "int32",
		"uint":   "uint32",
		"string": "string",
		"fd":     "uintptr",
		"fixed":  "float32",
		"array":  "[]int32",
	}

	wlNames        map[string]string
	constBuffer    bytes.Buffer
	ifaceBuffer    bytes.Buffer
	reqCodesBuffer bytes.Buffer
)

func main() {
	xmlFilePath, err := filepath.Abs("wayland.xml")
	if err != nil {
		log.Fatal(err)
	}

	xmlFile, err := os.Open(xmlFilePath)
	if err != nil {
		log.Fatal(err)
	}

	defer xmlFile.Close()

	var protocol Protocol
	if err := xml.NewDecoder(xmlFile).Decode(&protocol); err != nil {
		log.Fatal(err)
	}

	wlNames = make(map[string]string)

	constBuffer.WriteString("package wl")

	for _, iface := range protocol.Interfaces {
		//required for arg types
		registerAndCase(iface.Name)
	}

	reqCodesBuffer.WriteString("\n//Interface Request Codes\n") // request codes
	reqCodesBuffer.WriteString("\nconst (\n")                   // request codes
	for _, iface := range protocol.Interfaces {
		var eventBuffer bytes.Buffer
		var eventNames []string
		var ifaceName = wlNames[iface.Name]

		// Event struct types
		for _, event := range iface.Events {
			eventName := registerAndCase(event.Name)
			typeName := ifaceName + eventName + "Event"
			eventBuffer.WriteString(fmt.Sprintf("\ntype %s struct {\n", typeName))
			for _, arg := range event.Args {
				if t, ok := wlTypes[arg.Type]; ok { // if basic type
					eventBuffer.WriteString(fmt.Sprintf("%s %s\n", CamelCase(arg.Name), t))
				} else { // interface type
					if (arg.Type == "object" || arg.Type == "new_id") && arg.Interface != "" {
						t = "*" + wlNames[arg.Interface]
					} else {
						t = "Proxy"
					}
					eventBuffer.WriteString(fmt.Sprintf("%s %s\n", CamelCase(arg.Name), t))
				}
			}

			eventNames = append(eventNames, eventName)
			eventBuffer.WriteString("}\n")
		}

		eventBuffer.WriteTo(&ifaceBuffer)

		// interface type definition
		ifaceBuffer.WriteString(fmt.Sprintf("\ntype %s struct {\n", ifaceName))
		ifaceBuffer.WriteString("BaseProxy\n")
		for _, evName := range eventNames {
			ifaceBuffer.WriteString(fmt.Sprintf("%s chan %s\n", evName+"Chan", ifaceName+evName+"Event"))
		}
		ifaceBuffer.WriteString("}\n")

		// interface constructor
		ifaceBuffer.WriteString(fmt.Sprintf("\nfunc New%s(conn *Connection) *%s {\n", ifaceName, ifaceName))
		ifaceBuffer.WriteString(fmt.Sprintf("ret := new(%s)\n", ifaceName))
		for _, evName := range eventNames {
			ifaceBuffer.WriteString(fmt.Sprintf("ret.%s = make(chan %s)\n", evName+"Chan", ifaceName+evName+"Event"))
		}
		ifaceBuffer.WriteString("conn.Register(ret)\n")
		ifaceBuffer.WriteString("return ret\n")
		ifaceBuffer.WriteString("}\n")

		// interface method definitions (requests)
		// order used for request identification
		for order, req := range iface.Requests {
			reqName := CamelCase(req.Name)
			reqCodeName := strings.ToTitle(fmt.Sprintf("_%s_%s", ifaceName, reqName)) // first _ for not export constant
			reqCodesBuffer.WriteString(fmt.Sprintf("%s = %d\n", reqCodeName, order))

			ifaceBuffer.WriteString(fmt.Sprintf("\nfunc (p *%s) %s(", ifaceName, reqName))
			// get args buffer
			requestArgs(req).WriteTo(&ifaceBuffer)

			ifaceBuffer.WriteString(")") // close the args

			// get returns buffer
			requestRets(req).WriteTo(&ifaceBuffer)
			ifaceBuffer.WriteString("{\n")

			// get method body
			requestBody(req, reqCodeName).WriteTo(&ifaceBuffer)

			ifaceBuffer.WriteString("\n}\n")
		}

		// Enums - Constants
		for _, enum := range iface.Enums {
			enumName := registerAndCase(enum.Name)
			constTypeName := ifaceName + enumName
			constBuffer.WriteString(fmt.Sprintf("\ntype %s uint\n", constTypeName)) // enums are uint
			constBuffer.WriteString("const (\n")
			for _, entry := range enum.Entries {
				entryName := registerAndCase(entry.Name)
				constName := ifaceName + enumName + entryName
				constBuffer.WriteString(fmt.Sprintf("%s %s = %s\n", constName, constTypeName, entry.Value))
			}
			constBuffer.WriteString(")\n")
		}
	}
	reqCodesBuffer.WriteString(")") // request codes end

	constBuffer.WriteTo(os.Stdout)
	reqCodesBuffer.WriteTo(os.Stdout)
	ifaceBuffer.WriteTo(os.Stdout)
}

// register names to map
func registerAndCase(wlName string) string {
	var orj string = wlName
	wlName = CamelCase(wlName)
	wlNames[orj] = wlName
	return wlName
}

// only cases
func CamelCase(wlName string) string {
	if strings.HasPrefix(wlName, "wl_") {
		wlName = strings.TrimPrefix(wlName, "wl_")
	}

	// replace all "_" chars to " " chars
	wlName = strings.Replace(wlName, "_", " ", -1)

	// Capitalize first chars
	wlName = strings.Title(wlName)

	// remove all spaces
	wlName = strings.Replace(wlName, " ", "", -1)

	return wlName
}

func requestArgs(req Request) *bytes.Buffer {
	var (
		args       []string
		argsBuffer bytes.Buffer
	)

	for _, arg := range req.Args {
		// special type, for example registry.bind
		if arg.Type == "new_id" {
			if arg.Interface == "" {
				args = append(args, "iface string")
				args = append(args, "version uint32")
				args = append(args, fmt.Sprintf("%s Proxy", arg.Name))
			} else {
				continue
			}
		} else if arg.Type == "object" && arg.Interface != "" {
			argTypeName := wlNames[arg.Interface]
			args = append(args, fmt.Sprintf("%s *%s", arg.Name, argTypeName))
		} else {
			args = append(args, fmt.Sprintf("%s %s", arg.Name, wlTypes[arg.Type]))
		}
	}

	for i, arg := range args {
		if i > 0 {
			argsBuffer.WriteString(",")
		}
		argsBuffer.WriteString(arg)
	}

	return &argsBuffer
}

func requestRets(req Request) *bytes.Buffer {
	var (
		rets       []string
		retsBuffer bytes.Buffer
	)

	for _, arg := range req.Args {
		if arg.Type == "new_id" && arg.Interface != "" {
			retTypeName := wlNames[arg.Interface]
			rets = append(rets, fmt.Sprintf("*%s", retTypeName))
		}
	}

	// all request have an error return
	rets = append(rets, " error")

	if len(rets) > 1 {
		retsBuffer.WriteString("(")
	}

	for i, ret := range rets {
		if i > 0 {
			retsBuffer.WriteString(",")
		}
		retsBuffer.WriteString(ret)
	}

	if len(rets) > 1 {
		retsBuffer.WriteString(")")
	}

	return &retsBuffer
}

func requestBody(req Request, reqCodeName string) *bytes.Buffer {
	var (
		params       []string
		bodyBuffer   bytes.Buffer
		paramsBuffer bytes.Buffer
		hasRetType   string
	)

	for _, arg := range req.Args {
		if arg.Type == "new_id" {
			if arg.Interface != "" {
				retTypeName := wlNames[arg.Interface]
				bodyBuffer.WriteString(fmt.Sprintf("ret := New%s(p.Connection())\n", retTypeName))
				params = append(params, "Proxy(ret)")
				hasRetType = "ret,"
			} else {
				params = append(params, "iface")
				params = append(params, "version")
				params = append(params, arg.Name)
			}
		} else {
			params = append(params, arg.Name)
		}
	}

	for _, param := range params {
		paramsBuffer.WriteString(fmt.Sprintf(",%s", param))
	}

	bodyBuffer.WriteString(fmt.Sprintf("return %s p.Connection().SendRequest(p,%s%s)", hasRetType, reqCodeName, paramsBuffer.String()))

	return &bodyBuffer
}
