from pathlib import Path
p=Path('cq/windows.go');s=p.read_text();s=s.replace('code = strings.Repeat(" ", len(source))\n\t\tcode = windowKeepNewlines(code, source)', 'code = []byte(source)\n\t\tfor i := range code {\n\t\t\tif code[i] != \'\\n\' && code[i] != \'\\r\' { code[i] = \' \' }\n\t\t}')
s=s.replace('code, _ = windowRedact(code)', 'windowRedactBytes(code)')
s=s.replace('Code: Chunk{code[start:stop], line, last}', 'Code: Chunk{string(code[start:stop]), line, last}')
a=s.index('func windowKeepNewlines(');b=s.index('// windowBlank shares',a)
s=s[:a]+'''// windowRedactBytes masks the already-owned code buffer without a whole-file
// string copy. Window construction copies only bounded slices out of it.
func windowRedactBytes(b []byte) {
 for _, p := range secretPatterns {
  for _, m := range p.FindAllIndex(b, -1) {
   for i := m[0]; i < m[1]; i++ {
    if b[i] != '\\n' && b[i] != '\\r' { b[i] = '#' }
   }
  }
 }
}

'''+s[b:]
s=s.replace('func windowBlank(source string, l *Lang) (string, [][2]int, bool) {\n\tvar b []byte', 'func windowBlank(source string, l *Lang) ([]byte, [][2]int, bool) {\n\tb := []byte(source)')
s=s.replace('\t\tif b == nil {\n\t\t\tb = []byte(source)\n\t\t}\n\t\tspans', '\t\tspans')
s=s.replace('\tif b == nil {\n\t\treturn source, spans, lost || quote != 0\n\t}\n\treturn string(b), spans, lost || quote != 0', '\treturn b, spans, lost || quote != 0')
p.write_text(s)
p=Path('cq/windows_test.go');s=p.read_text().replace('got, spans, lost := windowBlank(tt.src, tb.Langs[tt.lang])', 'blanked, spans, lost := windowBlank(tt.src, tb.Langs[tt.lang])\n\t\tgot := string(blanked)');p.write_text(s)
