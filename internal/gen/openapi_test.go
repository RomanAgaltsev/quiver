package gen

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDocumentReadsAV3Spec(t *testing.T) {
	data, err := os.ReadFile("testdata/minimal.yaml")
	require.NoError(t, err)

	doc, err := LoadDocument(data)
	require.NoError(t, err)
	require.NotNil(t, doc)
	require.Equal(t, "Minimal", doc.Info.Title)
	require.Len(t, doc.Servers, 1)
}

func TestLoadDocumentRejectsSwagger2ByName(t *testing.T) {
	data, err := os.ReadFile("testdata/swagger2.yaml")
	require.NoError(t, err)

	_, err = LoadDocument(data)
	require.Error(t, err)
	require.Contains(t, err.Error(), "2.0",
		"the error must name the version so the user knows what to convert")
}

func TestLoadDocumentReportsBrokenRefWithAPointer(t *testing.T) {
	broken := []byte(`
openapi: 3.1.0
info: {title: Broken, version: "1.0"}
paths:
  /x:
    get:
      responses:
        "200":
          $ref: "#/components/responses/DoesNotExist"
`)
	_, err := LoadDocument(broken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "DoesNotExist",
		"a broken $ref must name what could not be resolved")
}
