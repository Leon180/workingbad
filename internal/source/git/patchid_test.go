package git

import "testing"

// The three rewrite scenarios we MUST collapse to a stable id: amend
// (same content, different blob hash), rebase (line numbers shift in hunk
// header), reorder (no header difference at all — just trivially stable).

const diffOriginal = `diff --git a/foo.go b/foo.go
index abc123..def456 100644
--- a/foo.go
+++ b/foo.go
@@ -10,3 +10,4 @@ func Foo() {
 	a := 1
-	b := 2
+	b := 3
+	c := 4
 	return a + b
`

// Same diff content, different "index" hashes — what amend produces.
const diffAfterAmend = `diff --git a/foo.go b/foo.go
index 1111aa..2222bb 100644
--- a/foo.go
+++ b/foo.go
@@ -10,3 +10,4 @@ func Foo() {
 	a := 1
-	b := 2
+	b := 3
+	c := 4
 	return a + b
`

// Same content, but rebase shifted the hunk down 50 lines (different @@ -X,Y).
const diffAfterRebase = `diff --git a/foo.go b/foo.go
index abc123..def456 100644
--- a/foo.go
+++ b/foo.go
@@ -60,3 +60,4 @@ func Foo() {
 	a := 1
-	b := 2
+	b := 3
+	c := 4
 	return a + b
`

// Different content — must differ.
const diffOtherContent = `diff --git a/foo.go b/foo.go
index abc123..def456 100644
--- a/foo.go
+++ b/foo.go
@@ -10,3 +10,4 @@ func Foo() {
 	a := 1
-	b := 2
+	b := 99
+	c := 4
 	return a + b
`

func TestPatchID_Stable_SameDiff(t *testing.T) {
	a, okA := PatchID([]byte(diffOriginal))
	b, okB := PatchID([]byte(diffOriginal))
	if !okA || !okB {
		t.Fatalf("ok=%v,%v", okA, okB)
	}
	if a != b {
		t.Errorf("idempotency broken: %q != %q", a, b)
	}
	if len(a) != 40 {
		t.Errorf("sha1 hex length = %d, want 40", len(a))
	}
}

func TestPatchID_AmendCollapses(t *testing.T) {
	a, _ := PatchID([]byte(diffOriginal))
	b, _ := PatchID([]byte(diffAfterAmend))
	if a != b {
		t.Errorf("amend produced different patch-id: %q vs %q (should collapse)", a, b)
	}
}

func TestPatchID_RebaseCollapses(t *testing.T) {
	a, _ := PatchID([]byte(diffOriginal))
	b, _ := PatchID([]byte(diffAfterRebase))
	if a != b {
		t.Errorf("rebase (line-shift) produced different patch-id: %q vs %q (should collapse)", a, b)
	}
}

func TestPatchID_DifferentContent(t *testing.T) {
	a, _ := PatchID([]byte(diffOriginal))
	b, _ := PatchID([]byte(diffOtherContent))
	if a == b {
		t.Errorf("different content yielded same patch-id %q (collision)", a)
	}
}

func TestPatchID_EmptyOrMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "   \n\n  \t"},
		{"no diff header", "some random text\nno diff git line\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := PatchID([]byte(tt.in)); ok {
				t.Errorf("expected !ok for %q", tt.in)
			}
		})
	}
}

func TestPatchID_BinarySentinel(t *testing.T) {
	const binDiff = `diff --git a/img.png b/img.png
index aaa..bbb 100644
Binary files a/img.png and b/img.png differ
`
	id, ok := PatchID([]byte(binDiff))
	if !ok {
		t.Fatal("binary diff should produce an id (BIN sentinel)")
	}
	if len(id) != 40 {
		t.Errorf("binary id length = %d, want 40", len(id))
	}
}

func TestPatchID_MultipleFiles(t *testing.T) {
	const multi = `diff --git a/foo.go b/foo.go
index abc..def 100644
--- a/foo.go
+++ b/foo.go
@@ -1,1 +1,1 @@
-old
+new
diff --git a/bar.go b/bar.go
index 111..222 100644
--- a/bar.go
+++ b/bar.go
@@ -1,1 +1,1 @@
-old2
+new2
`
	id, ok := PatchID([]byte(multi))
	if !ok {
		t.Fatal("multi-file diff should produce id")
	}
	// A change to the second file alone should produce a different id.
	idFirstOnly, _ := PatchID([]byte(`diff --git a/foo.go b/foo.go
index abc..def 100644
--- a/foo.go
+++ b/foo.go
@@ -1,1 +1,1 @@
-old
+new
`))
	if id == idFirstOnly {
		t.Errorf("multi-file id collapsed to single-file id: %q", id)
	}
}
