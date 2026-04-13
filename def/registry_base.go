package def

import (
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/antchfx/xmlquery"
	"github.com/tidwall/gjson"
)

type TypeCategory int

const (
	CatNone TypeCategory = iota

	CatExten // meta-category for printing extension names/version constants

	CatDefine
	CatInclude
	CatExternal

	CatHandle
	CatBasetype
	CatEnum
	CatBitmask

	CatStruct
	CatUnion

	CatPointer
	CatArray

	CatCommand

	CatMaximum
)

// only this type needs strings, so define here to
// remove dependency on stringer utility
var typeCategoryStrings = map[TypeCategory]string{
	CatNone:     "CatNone",
	CatExten:    "CatExten",
	CatDefine:   "CatDefine",
	CatInclude:  "CatInclude",
	CatExternal: "CatExternal",
	CatHandle:   "CatHandle",
	CatBasetype: "CatBasetype",
	CatEnum:     "CatEnum",
	CatBitmask:  "CatBitmask",
	CatStruct:   "CatStruct",
	CatUnion:    "CatUnion",
	CatPointer:  "CatPointer",
	CatArray:    "CatArray",
	CatCommand:  "CatCommand",
	CatMaximum:  "CatMaximum",
}

func (tc TypeCategory) String() string {
	if str, ok := typeCategoryStrings[tc]; ok {
		return str
	}
	slog.Error("invalid TypeCategory", "typeCategory", tc)
	return ""
}

// Filename returns an output filename for each category.
func (tc TypeCategory) Filename(platformName string) (filename string) {
	filename = strings.ToLower(strings.TrimPrefix(tc.String(), "Cat"))
	switch tc {
	case CatCommand:
		switch platformName {
		case "":
			// needs cgo and syscall.
		case "win32":
			filename += "_" + platformName + "_syscall"
		default:
			filename += "_" + platformName + "_cgo"
		}
	default:
		if platformName != "" {
			filename += "_" + platformName
		}
	}
	return filename
}

type fnReadFromXML func(doc *xmlquery.Node, tr TypeRegistry, vr ValueRegistry, api string)
type fnReadFromJSON func(exceptions gjson.Result, tr TypeRegistry, vr ValueRegistry)

func (c TypeCategory) ReadFns() (fnReadFromXML, fnReadFromJSON) {
	switch c {

	case CatDefine:
		return ReadDefineTypesFromXML, ReadDefineExceptionsFromJSON
	case CatInclude:
		return ReadIncludeTypesFromXML, ReadIncludeExceptionsFromJSON
	case CatExternal:
		return ReadExternalTypesFromXML, ReadExternalExceptionsFromJSON

	case CatHandle:
		return ReadHandleTypesFromXML, ReadHandleExceptionsFromJSON
	case CatBasetype:
		return ReadBaseTypesFromXML, ReadBaseTypeExceptionsFromJSON
	case CatBitmask:
		return ReadBitmaskTypesFromXML, nil
	case CatEnum:
		return ReadEnumTypesFromXML, nil
	// // case CatStatic:

	case CatStruct:
		return ReadStructTypesFromXML, ReadStructExceptionsFromJSON
	case CatUnion:
		return ReadUnionTypesFromXML, ReadUnionExceptionsFromJSON

	case CatPointer:
		return nil, nil
	case CatArray:
		return nil, nil

	case CatCommand:
		return ReadCommandTypesFromXML, ReadCommandExceptionsFromJSON

	default:
		return nil, nil
	}
}

type TypeRegistry map[string]TypeDefiner
type ValueRegistry map[string]ValueDefiner

func (tr TypeRegistry) SelectCategory(cat TypeCategory) *IncludeSet {
	rval := NewIncludeSet()
	for k, v := range tr {
		if v.Category() == cat {
			rval.IncludeTypes[k] = true
		}
	}
	return rval
}

type Namer interface {
	RegistryName() string
	PublicName() string
	InternalName() string

	Aliaser
	Resolver
}

type Aliaser interface {
	SetAliasType(TypeDefiner)
	IsAlias() bool
}

type Resolver interface {
	Resolve(TypeRegistry, ValueRegistry) *IncludeSet
	IsIdenticalPublicAndInternal() bool
}

type TypeDefiner interface {
	Category() TypeCategory
	Namer
	Resolver
	Printer

	AllValues() []ValueDefiner
	PushValue(ValueDefiner)
	AppendValues(vals ValueRegistry)
}

type Printer interface {
	RegisterImports(reg map[string]bool)
	PrintPublicDeclaration(io.Writer)
	PrintInternalDeclaration(io.Writer)
	PrintPublicToInternalTranslation(w io.Writer, inputVar, outputVar, lenSpec string)
	PrintTranslateToInternal(w io.Writer, inputVar, outputVar string)
	TranslateToPublic(inputVar string) string
	TranslateToInternal(inputVar string) string
}

type ImportMap map[string]bool

func (m ImportMap) SortedKeys() []string {
	rval := make([]string, 0, len(m))
	for k := range m {
		rval = append(rval, k)
	}
	sort.Sort(sort.StringSlice(rval))
	return rval
}

type ByName []TypeDefiner

func (a ByName) Len() int           { return len(a) }
func (a ByName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByName) Less(i, j int) bool { return a[i].PublicName() < a[j].PublicName() }

type ValueDefiner interface {
	RegistryName() string
	PublicName() string

	ValueString() string
	UnderlyingTypeName() string
	ResolvedType() TypeDefiner

	Resolve(TypeRegistry, ValueRegistry) *IncludeSet

	PrintPublicDeclaration(w io.Writer)
	SetExtensionNumber(int)

	IsAlias() bool
	IsCore() bool
}

type ByValue []ValueDefiner

// TODO: also sort by bitmask values and aliases
func (a ByValue) Len() int      { return len(a) }
func (a ByValue) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a ByValue) Less(i, j int) bool {
	iNum, err1 := strconv.Atoi(a[i].ValueString())
	jNum, err2 := strconv.Atoi(a[j].ValueString())
	if err1 == nil && err2 == nil {
		if iNum == jNum {
			return a[i].RegistryName() < a[j].RegistryName()
		}
		return iNum < jNum
	}
	if a[i].ValueString() == a[j].ValueString() {
		return a[i].RegistryName() < a[j].RegistryName()
	}
	return a[i].ValueString() < a[j].ValueString()
}

type ByValuePublicName []ValueDefiner // add for cleanup/issue-3

func (a ByValuePublicName) Len() int      { return len(a) }
func (a ByValuePublicName) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a ByValuePublicName) Less(i, j int) bool {
	iNum, err1 := strconv.Atoi(a[i].PublicName())
	jNum, err2 := strconv.Atoi(a[j].PublicName())
	if err1 == nil && err2 == nil {
		return iNum < jNum
	}
	return a[i].PublicName() < a[j].PublicName()
}
