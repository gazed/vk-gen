package main

import (
	"flag"
	"fmt"
	"go/build"
	"io"
	"io/fs"
	"io/ioutil"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/antchfx/xmlquery"
	"github.com/gazed/vk-gen/def"
	"github.com/gazed/vk-gen/feat"
	"github.com/tidwall/gjson"
)

var (
	inFileName         string   // vulkan spec.
	outDirName         string   // output directory for vulkan bindings.
	apiName            string   // which vulkan spec
	platformTargets    string   // desired vulkan platforms as comma separated string.
	separatedPlatforms []string // desired vulkan platforms
	goimportsPath      string   // require goimports executable.
)

// init is run, automatically by go, once on startup.
func init() {
	flag.StringVar(&inFileName, "inFile", "vk.xml", "Vulkan XML registry file to read")
	flag.StringVar(&outDirName, "outDir", "vk", "Directory to write go-vk output to")
	flag.StringVar(&apiName, "api", "vulkan", "API to generate against; possible values include 'vulkan' and 'vulkansc'")
	flag.StringVar(&platformTargets, "platform", "win32,metal", "Comma-separated list of platforms to generate for; this looks at the Vulkan name, not the GOOS name for the platform")
	flag.Parse()
}

// main in the standard startup execution entry point.
func main() {

	// goimports is required to clean up the generated bindings.
	var err error
	goimportsPath, err = findGoExecutable("goimports")
	if err != nil {
		slog.Error("Could not find goimports", "error", err.Error())
		return
	}

	// find or create the output directory.
	_, err = os.Stat(outDirName)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.Mkdir(outDirName, 0777|fs.ModeDir); err != nil {
				slog.Error("Could not create output directory", "error", err)
				return
			} else {
				slog.Info("Output directory created", "directory", outDirName)
			}
		}
	}

	// open the vulkan xml spec and prep for xpath queries.
	f, err := os.Open(inFileName)
	if err != nil {
		slog.Error("Could not open Vulkan registry file", "filename", inFileName, "error", err)
		return
	}
	defer f.Close()
	xmlDoc, err := xmlquery.Parse(f)
	if err != nil {
		slog.Error("Could not parse XML from the provided file", "filename", inFileName, "error", err)
		return
	}

	// parse the desired vulkan platforms.
	separatedPlatforms = strings.Split(platformTargets, ",")
	if len(separatedPlatforms) == 0 {
		slog.Info("Generating core Vulkan only; no platform specific extensions will be available!")
	} else {
		slog.Info(fmt.Sprintf("Found %d platforms to generate for", len(separatedPlatforms)), "platforms", separatedPlatforms)
	}

	// open the spec overrides.
	exceptionsBytes, err := os.ReadFile("exceptions.json")
	if err != nil {
		slog.Error("Could not parse json from exceptions.json", "error", err)
	}
	jsonDoc := gjson.ParseBytes(exceptionsBytes)

	// parse the spec data into structures.
	globalTypes := make(def.TypeRegistry)
	globalValues := make(def.ValueRegistry)
	for tc := def.CatNone; tc < def.CatMaximum; tc++ {
		xml, json := tc.ReadFns()
		if xml != nil {
			xml(xmlDoc, globalTypes, globalValues, apiName)
		}
		if json != nil {
			json(jsonDoc, globalTypes, globalValues)
		}
	}

	// parse the vulkan platforms.
	platforms := make(feat.PlatformRegistry)
	platforms[""] = feat.NewGeneralPlatform()
	for _, n := range xmlquery.Find(xmlDoc, "//platforms/platform") {
		plat := feat.NewPlatformFromXML(n)
		platforms[plat.Name()] = plat
	}
	jsonDoc.Get("platform").ForEach(func(key, value gjson.Result) bool {
		if key.String() == "!comment" {
			return true
		}
		r := feat.NewOrUpdatePlatformFromJSON(key.String(), value, platforms[key.String()])
		platforms[r.Name()] = r
		return true
	})

	// parse the vulkan feature releases.
	// This does not work with vulkan 1.4 spec as VK_VERSION_1_0 changes to VK_BASE_VERSION_1_0
	vk1_0 := feat.ReadFeatureFromXML(xmlquery.FindOne(xmlDoc, "//feature[@name='VK_VERSION_1_0']"), globalTypes, globalValues)
	vk1_1 := feat.ReadFeatureFromXML(xmlquery.FindOne(xmlDoc, "//feature[@name='VK_VERSION_1_1']"), globalTypes, globalValues)
	vk1_2 := feat.ReadFeatureFromXML(xmlquery.FindOne(xmlDoc, "//feature[@name='VK_VERSION_1_2']"), globalTypes, globalValues)
	vk1_3 := feat.ReadFeatureFromXML(xmlquery.FindOne(xmlDoc, "//feature[@name='VK_VERSION_1_3']"), globalTypes, globalValues)
	vk1_0.MergeWith(vk1_1)
	vk1_0.MergeWith(vk1_2)
	vk1_0.MergeWith(vk1_3)

	// Manually include external types
	vk1_0.MergeIncludeSet(globalTypes.SelectCategory(def.CatExternal))

	// get platform extensions
	for _, platName := range separatedPlatforms {
		xpath := fmt.Sprintf("//extension[@platform='%s']", platName)
		for _, extNode := range xmlquery.Find(xmlDoc, xpath) {
			ext := feat.ReadExtensionFromXML(extNode, globalTypes, globalValues)
			platforms[ext.PlatformName()].IncludeExtension(ext)
		}
	}

	// get "Core" extensions
	extQueryString := fmt.Sprintf("//extension[not(@platform) and contains(@supported,'%s')]", apiName)
	for _, extNode := range xmlquery.Find(xmlDoc, extQueryString) {
		ext := feat.ReadExtensionFromXML(extNode, globalTypes, globalValues)
		platforms[""].IncludeExtension(ext)
	}
	vk1_0.MergeWith(platforms[""].GeneratePlatformFeatures())
	vk1_0.Resolve(globalTypes, globalValues)

	// generate standard bindings
	for tc, reg := range vk1_0.FilterByCategory() {
		if tc == def.CatHandle {
			// Special case...VK_NULL_HANDLE is included by vk.xml as a type, not an enum. vk-gen treats it as a
			// ValueDefiner, so it must be manually added to the feature registry.
			globalValues["VK_NULL_HANDLE"].Resolve(globalTypes, globalValues)
			reg.ResolvedTypes["VK_DEFINE_HANDLE"].PushValue(globalValues["VK_NULL_HANDLE"])
		}
		printCategory(tc, reg, nil)

		// generate a second command file using syscall.
		if tc == def.CatCommand {
			def.CommandIsSyscall = true
			printCategory(tc, reg, nil)  // currently windows only.
			def.CommandIsSyscall = false // set back to default when done.
		}
	}

	// generate platform specific bindings
	for pName, plat := range platforms {
		if pName == "" {
			continue
		}
		pf := plat.GeneratePlatformFeatures()
		pf.Resolve(globalTypes, globalValues)
		for tc, reg := range pf.FilterByCategory() {
			if tc == def.CatCommand && pName == "win32" {
				def.CommandIsSyscall = true
			}
			printCategory(tc, reg, plat)
			if tc == def.CatCommand && pName == "win32" {
				def.CommandIsSyscall = false // set back to default when done.
			}
		}
	}

	// copy binding helper files.
	copyStaticFiles()
}

// printCategory
func printCategory(tc def.TypeCategory, fc *feat.Feature, platform *feat.Platform) {
	if tc == def.CatInclude {
		return
	}
	reg := fc.ResolvedTypes
	if len(reg) == 0 && len(fc.ResolvedValues) == 0 {
		slog.Warn("No resolved types or values for category", "category", tc.String())
		return // warn, because this is not expected to happen.
	}

	// create a binding output file per category and platform combination.
	platformName := ""
	if platform != nil {
		platformName = platform.Name()
	}
	filename := tc.Filename(platformName)

	// platform independent commands bindings can either be generated using syscall or cgo
	// Currently only windows supports syscall.
	if tc == def.CatCommand && platform == nil {
		if def.CommandIsSyscall {
			filename = filename + "_syscall"
		} else {
			filename = filename + "_cgo"
		}
	}
	outpath := fmt.Sprintf("%s/%s", outDirName, filename+".go")
	f, _ := os.Create(outpath) // explicit f.Close() is below.
	// file close is not deferred because
	// the file must be written to disk before goimports is run

	// hide platforms behind go:build directives.
	if platform != nil && platform.GoBuildTag != "" && tc != def.CatEnum && tc != def.CatBitmask {
		fmt.Fprintf(f, "//go:build %s\n", platform.GoBuildTag)
	} else if tc == def.CatCommand && platform == nil {
		// ensure the core command compile syscall on windows
		// and cgo command everywhere else but windows.
		if def.CommandIsSyscall {
			fmt.Fprintf(f, "//go:build windows\n")
		} else {
			fmt.Fprintf(f, "//go:build !windows\n")
		}
	}

	// first line of each file.
	const fileHeader string = "// Code generated by go-vk from %s. DO NOT EDIT.\n\npackage vk\n\n"
	fmt.Fprintf(f, fileHeader, inFileName)

	// add in
	if platform != nil && len(platform.GoImports) > 0 {
		fmt.Fprintf(f, "import (\n")
		for _, i := range platform.GoImports {
			fmt.Fprintf(f, "\"%s\"", i)
		}
		fmt.Fprintf(f, ")\n")
	}

	// add C import for cgo based command files.
	if tc == def.CatCommand && !def.CommandIsSyscall {
		fmt.Fprintf(f, "\n\n")
		fmt.Fprintf(f, "// #include <stdlib.h>\n")
		fmt.Fprintf(f, "// #include \"dlload.h\"\n")
		fmt.Fprintf(f, "import \"C\"\n\n\n")
	}

	types := make([]def.TypeDefiner, 0, len(reg))
	for _, v := range reg {
		types = append(types, v)
		v.AppendValues(fc.ResolvedValues[v.RegistryName()])
		delete(fc.ResolvedValues, v.RegistryName())
	}

	sort.Sort(def.ByName(types))

	importMap := make(def.ImportMap)
	for _, t := range types {
		t.RegisterImports(importMap)
	}
	if len(importMap) > 0 {
		keys := importMap.SortedKeys()
		fmt.Fprint(f, "import (\n")
		for _, k := range keys {
			fmt.Fprintf(f, "  \"%s\"\n", k)
		}
		fmt.Fprintln(f, ")")
		fmt.Fprintln(f)
	}
	printTypes(f, types, fc.ResolvedValues)
	printLooseValues(f, fc.ResolvedValues)

	// write the binding file and run goimports on it.
	f.Close()
	slog.Info("Running goimports", "file", filename+".go")
	cmd := exec.Command(goimportsPath, "-w", outpath)
	e := &strings.Builder{}
	cmd.Stderr = e
	goimpErr := cmd.Run()
	if goimpErr != nil {
		slog.Error("Failed to format source file",
			"path", outpath,
			"error", goimpErr.Error(),
			"goimports output", e.String())
	}
}

// printTypes
func printTypes(w io.Writer, types []def.TypeDefiner, vals map[string]def.ValueRegistry) {
	globalBuf := &strings.Builder{}
	initBuf := &strings.Builder{}
	contentBuf := &strings.Builder{}
	for _, v := range types {
		if strings.HasPrefix(v.PublicName(), "!") {
			continue
		}
		v.PrintPublicDeclaration(contentBuf)
		v.PrintInternalDeclaration(contentBuf)
	}
	if globalBuf.Len() > 0 {
		fmt.Fprintf(w, "const (\n")
		fmt.Fprint(w, globalBuf.String())
		fmt.Fprintf(w, ")\n\n")
	}
	if initBuf.Len() > 0 {
		fmt.Fprint(w, "func init() {\n")
		fmt.Fprint(w, initBuf.String())
		fmt.Fprint(w, "}\n\n")
	}
	fmt.Fprint(w, contentBuf.String())
}

func printLooseValues(w io.Writer, valsByTypeName map[string]def.ValueRegistry) {
	for k, vr := range valsByTypeName {

		// Values will be sorted by const name for extension names/spec versions,
		// and by value for typed consts
		allValues := make([]def.ValueDefiner, 0, len(vr))
		for _, val := range vr {
			allValues = append(allValues, val)
		}
		if k == "" {
			fmt.Fprint(w, "// Extension names and versions\n")

			// Drop the values into a slice and sort by the const name
			sort.Sort(def.ByValuePublicName(allValues))
		} else {
			fmt.Fprintf(w, "// Platform-specific values for %s\n", k)
			sort.Sort(def.ByValue(allValues))
		}
		fmt.Fprintf(w, "const (\n")
		for _, val := range allValues {
			val.PrintPublicDeclaration(w)
		}
		fmt.Fprintf(w, ")\n\n")
	}
}

// copyStaticFiles copies all the files from the source directory
// to the output binding directory.
func copyStaticFiles() {
	slog.Info("Copying static files")
	source := "static_include"

	// Naive solution from https://stackoverflow.com/questions/51779243/copy-a-folder-in-go
	var err error = filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		var relPath string = strings.Replace(path, source, "", 1)
		if relPath == "" {
			return nil
		}
		if info.IsDir() {
			return os.Mkdir(filepath.Join(outDirName, relPath), 0777)
		} else {
			var data, err1 = ioutil.ReadFile(filepath.Join(source, relPath))
			if err1 != nil {
				return err1
			}
			return ioutil.WriteFile(filepath.Join(outDirName, relPath), data, 0666)
		}
	})
	if err != nil {
		panic(err)
	}
}

// findGoExecutable looks for the named executable assuming that
// the user has not added "go env GOPATH"/bin to shell PATH
func findGoExecutable(name string) (path string, err error) {

	// probably in GOPATH which may not be in the user's PATH
	goPath := os.Getenv("GOPATH")
	if goPath == "" {
		goPath = build.Default.GOPATH // use go default if not set.
	}

	// There may be multiple paths, so split and add "/bin" to each
	paths := strings.Split(goPath, string(os.PathListSeparator))
	goPath = ""
	for _, path := range paths {
		goPath += fmt.Sprintf("%s%sbin%s", path, string(os.PathSeparator), string(os.PathListSeparator))
	}

	// Add PATH paths to the end
	goPath += os.Getenv("PATH")
	os.Setenv("PATH", goPath)
	return exec.LookPath(name)
}
