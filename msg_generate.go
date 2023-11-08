//go:build ignore
// +build ignore

// msg_generate.go is meant to run with go generate. It will use
// go/{importer,types} to track down all the RR struct types. Then for each type
// it will generate pack/unpack methods based on the struct tags. The generated source is
// written to zmsg.go, and is meant to be checked into git.
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"go/types"
	"log"
	"os"
	"strings"

	"golang.org/x/tools/go/packages"
)

var packageHdr = `
// Code generated by "go run msg_generate.go"; DO NOT EDIT.

package dns

import "golang.org/x/crypto/cryptobyte"

`

// getTypeStruct will take a type and the package scope, and return the
// (innermost) struct if the type is considered a RR type (currently defined as
// those structs beginning with a RR_Header, could be redefined as implementing
// the RR interface). The bool return value indicates if embedded structs were
// resolved.
func getTypeStruct(t types.Type, scope *types.Scope) (*types.Struct, bool) {
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return nil, false
	}
	if st.NumFields() == 0 {
		return nil, false
	}
	if st.Field(0).Type() == scope.Lookup("RR_Header").Type() {
		return st, false
	}
	if st.Field(0).Anonymous() {
		st, _ := getTypeStruct(st.Field(0).Type(), scope)
		return st, true
	}
	return nil, false
}

// loadModule retrieves package description for a given module.
func loadModule(name string) (*types.Package, error) {
	conf := packages.Config{Mode: packages.NeedTypes | packages.NeedTypesInfo}
	pkgs, err := packages.Load(&conf, name)
	if err != nil {
		return nil, err
	}
	return pkgs[0].Types, nil
}

func main() {
	// Import and type-check the package
	pkg, err := loadModule("github.com/miekg/dns")
	fatalIfErr(err)
	scope := pkg.Scope()

	// Collect actual types (*X)
	var namedTypes []string
	for _, name := range scope.Names() {
		o := scope.Lookup(name)
		if o == nil || !o.Exported() {
			continue
		}
		if st, _ := getTypeStruct(o.Type(), scope); st == nil {
			continue
		}
		if name == "PrivateRR" {
			continue
		}

		// Check if corresponding TypeX exists
		if scope.Lookup("Type"+o.Name()) == nil && o.Name() != "RFC3597" {
			log.Fatalf("Constant Type%s does not exist.", o.Name())
		}

		namedTypes = append(namedTypes, o.Name())
	}

	b := &bytes.Buffer{}
	b.WriteString(packageHdr)

	fmt.Fprint(b, "// pack*() functions\n\n")
	for _, name := range namedTypes {
		o := scope.Lookup(name)
		st, _ := getTypeStruct(o.Type(), scope)

		fmt.Fprintf(b, "func (rr *%s) pack(msg []byte, off int, compression compressionMap, compress bool) (off1 int, err error) {\n", name)
		for i := 1; i < st.NumFields(); i++ {
			o := func(s string) {
				fmt.Fprintf(b, s, st.Field(i).Name())
				fmt.Fprint(b, `if err != nil {
return off, err
}
`)
			}

			if _, ok := st.Field(i).Type().(*types.Slice); ok {
				switch st.Tag(i) {
				case `dns:"-"`: // ignored
				case `dns:"txt"`:
					o("off, err = packStringTxt(rr.%s, msg, off)\n")
				case `dns:"opt"`:
					o("off, err = packDataOpt(rr.%s, msg, off)\n")
				case `dns:"nsec"`:
					o("off, err = packDataNsec(rr.%s, msg, off)\n")
				case `dns:"pairs"`:
					o("off, err = packDataSVCB(rr.%s, msg, off)\n")
				case `dns:"domain-name"`:
					o("off, err = packDataDomainNames(rr.%s, msg, off, compression, false)\n")
				case `dns:"apl"`:
					o("off, err = packDataApl(rr.%s, msg, off)\n")
				default:
					log.Fatalln(name, st.Field(i).Name(), st.Tag(i))
				}
				continue
			}

			switch {
			case st.Tag(i) == `dns:"-"`: // ignored
			case st.Tag(i) == `dns:"cdomain-name"`:
				o("off, err = packDomainName(rr.%s, msg, off, compression, compress)\n")
			case st.Tag(i) == `dns:"domain-name"`:
				o("off, err = packDomainName(rr.%s, msg, off, compression, false)\n")
			case st.Tag(i) == `dns:"a"`:
				o("off, err = packDataA(rr.%s, msg, off)\n")
			case st.Tag(i) == `dns:"aaaa"`:
				o("off, err = packDataAAAA(rr.%s, msg, off)\n")
			case st.Tag(i) == `dns:"uint48"`:
				o("off, err = packUint48(rr.%s, msg, off)\n")
			case st.Tag(i) == `dns:"txt"`:
				o("off, err = packString(rr.%s, msg, off)\n")

			case strings.HasPrefix(st.Tag(i), `dns:"size-base32`): // size-base32 can be packed just like base32
				fallthrough
			case st.Tag(i) == `dns:"base32"`:
				o("off, err = packStringBase32(rr.%s, msg, off)\n")

			case strings.HasPrefix(st.Tag(i), `dns:"size-base64`): // size-base64 can be packed just like base64
				fallthrough
			case st.Tag(i) == `dns:"base64"`:
				o("off, err = packStringBase64(rr.%s, msg, off)\n")

			case strings.HasPrefix(st.Tag(i), `dns:"size-hex:SaltLength`):
				// directly write instead of using o() so we get the error check in the correct place
				field := st.Field(i).Name()
				fmt.Fprintf(b, `// Only pack salt if value is not "-", i.e. empty
if rr.%s != "-" {
  off, err = packStringHex(rr.%s, msg, off)
  if err != nil {
    return off, err
  }
}
`, field, field)
				continue
			case strings.HasPrefix(st.Tag(i), `dns:"size-hex`): // size-hex can be packed just like hex
				fallthrough
			case st.Tag(i) == `dns:"hex"`:
				o("off, err = packStringHex(rr.%s, msg, off)\n")
			case st.Tag(i) == `dns:"any"`:
				o("off, err = packStringAny(rr.%s, msg, off)\n")
			case st.Tag(i) == `dns:"octet"`:
				o("off, err = packStringOctet(rr.%s, msg, off)\n")
			case st.Tag(i) == `dns:"ipsechost"` || st.Tag(i) == `dns:"amtrelayhost"`:
				o("off, err = packIPSECGateway(rr.GatewayAddr, rr.%s, msg, off, rr.GatewayType, compression, false)\n")
			case st.Tag(i) == "":
				switch st.Field(i).Type().(*types.Basic).Kind() {
				case types.Uint8:
					o("off, err = packUint8(rr.%s, msg, off)\n")
				case types.Uint16:
					o("off, err = packUint16(rr.%s, msg, off)\n")
				case types.Uint32:
					o("off, err = packUint32(rr.%s, msg, off)\n")
				case types.Uint64:
					o("off, err = packUint64(rr.%s, msg, off)\n")
				case types.String:
					o("off, err = packString(rr.%s, msg, off)\n")
				default:
					log.Fatalln(name, st.Field(i).Name())
				}
			default:
				log.Fatalln(name, st.Field(i).Name(), st.Tag(i))
			}
		}
		fmt.Fprint(b, "return off, nil }\n\n")
	}

	fmt.Fprint(b, "// unpack*() functions\n\n")
	for _, name := range namedTypes {
		o := scope.Lookup(name)
		st, _ := getTypeStruct(o.Type(), scope)

		fmt.Fprintf(b, "func (rr *%s) unpack(data, msgBuf []byte) (err error) {\n", name)
		fmt.Fprintln(b, "s := cryptobyte.String(data)")
	fieldLoop:
		for i := 1; i < st.NumFields(); i++ {
			errCheck := func() {
				fmt.Fprintln(b, "if err != nil { return err }")
			}
			unpackField := func(unpacker string) {
				fmt.Fprintf(b, "rr.%s, err = %s(&s)\n", st.Field(i).Name(), unpacker)
				errCheck()
			}
			unpackFieldBuf := func(unpacker string) {
				fmt.Fprintf(b, "rr.%s, err = %s(&s, msgBuf)\n", st.Field(i).Name(), unpacker)
				errCheck()
			}
			unpackFieldRest := func(unpacker string) {
				fmt.Fprintf(b, "rr.%s, err = %s(&s, len(s))\n", st.Field(i).Name(), unpacker)
				errCheck()
			}
			unpackFieldLength := func(unpacker, len string) {
				fmt.Fprintf(b, "rr.%s, err = %s(&s, int(rr.%s))\n", st.Field(i).Name(), unpacker, len)
				errCheck()
			}
			readInt := func(type_ string) {
				fmt.Fprintf(b, "if !s.Read%s(&rr.%s) { return errUnpackOverflow }\n", type_, st.Field(i).Name())
			}

			// size-* are special, because they reference a struct member we should use for the length.
			if strings.HasPrefix(st.Tag(i), `dns:"size-`) {
				structMember := structMember(st.Tag(i))
				structTag := structTag(st.Tag(i))
				switch structTag {
				case "hex":
					unpackFieldLength("unpackStringHex", structMember)
				case "base32":
					unpackFieldLength("unpackStringBase32", structMember)
				case "base64":
					unpackFieldLength("unpackStringBase64", structMember)
				default:
					log.Fatalln(name, st.Field(i).Name(), st.Tag(i))
				}
				continue
			}

			if _, ok := st.Field(i).Type().(*types.Slice); ok {
				switch st.Tag(i) {
				case `dns:"-"`: // ignored
					continue fieldLoop
				case `dns:"txt"`:
					unpackField("unpackStringTxt")
				case `dns:"opt"`:
					unpackField("unpackDataOpt")
				case `dns:"nsec"`:
					unpackField("unpackDataNsec")
				case `dns:"pairs"`:
					unpackField("unpackDataSVCB")
				case `dns:"domain-name"`:
					unpackFieldBuf("unpackDataDomainNames")
				case `dns:"apl"`:
					unpackField("unpackDataApl")
				default:
					log.Fatalln(name, st.Field(i).Name(), st.Tag(i))
				}
				continue
			}

			switch st.Tag(i) {
			case `dns:"-"`: // ignored
				continue fieldLoop
			case `dns:"cdomain-name"`, `dns:"domain-name"`:
				unpackFieldBuf("unpackDomainName")
			case `dns:"a"`:
				unpackField("unpackDataA")
			case `dns:"aaaa"`:
				unpackField("unpackDataAAAA")
			case `dns:"uint48"`:
				readInt("Uint48")
			case `dns:"txt"`:
				unpackField("unpackString")
			case `dns:"base32"`:
				unpackFieldRest("unpackStringBase32")
			case `dns:"base64"`:
				unpackFieldRest("unpackStringBase64")
			case `dns:"hex"`:
				unpackFieldRest("unpackStringHex")
			case `dns:"any"`:
				unpackFieldRest("unpackStringAny")
			case `dns:"octet"`:
				unpackField("unpackStringOctet")
			case `dns:"ipsechost"`, `dns:"amtrelayhost"`:
				// TODO(tmthrgd): This is a particular unpleasant
				// way of dealing with this. Can we do better?
				// Probably not with the structs as they are.
				fmt.Fprintln(b, "rr.GatewayAddr, rr.GatewayHost, err = unpackIPSECGateway(&s, msgBuf, rr.GatewayType)")
				errCheck()
			case "":
				switch st.Field(i).Type().(*types.Basic).Kind() {
				case types.Uint8:
					readInt("Uint8")
				case types.Uint16:
					readInt("Uint16")
				case types.Uint32:
					readInt("Uint32")
				case types.Uint64:
					readInt("Uint64")
				case types.String:
					unpackField("unpackString")
				default:
					log.Fatalln(name, st.Field(i).Name())
				}
			default:
				log.Fatalln(name, st.Field(i).Name(), st.Tag(i))
			}
			// If we've hit s.Empty() we return without error.
			if i < st.NumFields()-1 {
				fmt.Fprintln(b, "if s.Empty() { return nil }")
			}
		}
		fmt.Fprintln(b, "if !s.Empty() { return errTrailingRData }")
		fmt.Fprint(b, "return nil }\n\n")
	}

	// gofmt
	res, err := format.Source(b.Bytes())
	if err != nil {
		b.WriteTo(os.Stderr)
		log.Fatal(err)
	}

	// write result
	f, err := os.Create("zmsg.go")
	fatalIfErr(err)
	defer f.Close()
	f.Write(res)
}

// structMember will take a tag like dns:"size-base32:SaltLength" and return the last part of this string.
func structMember(s string) string {
	idx := strings.LastIndex(s, ":")
	return strings.TrimSuffix(s[idx+1:], `"`)
}

// structTag will take a tag like dns:"size-base32:SaltLength" and return base32.
func structTag(s string) string {
	s = strings.TrimPrefix(s, `dns:"size-`)
	s, _, _ = strings.Cut(s, ":")
	return s
}

func fatalIfErr(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
