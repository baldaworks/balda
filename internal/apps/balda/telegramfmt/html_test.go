package telegramfmt

import "testing"

func TestHTML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "escapes raw text",
			in:   "A < B & C > D",
			want: "A &lt; B &amp; C &gt; D",
		},
		{
			name: "preserves supported tags and escapes text inside",
			in:   "<b>Build:</b> <code>x < y & y > z</code>",
			want: "<b>Build:</b> <code>x &lt; y &amp; y &gt; z</code>",
		},
		{
			name: "preserves existing telegram entities",
			in:   "A &lt; B &amp; C &#35; &#x23; &#X23;",
			want: "A &lt; B &amp; C &#35; &#x23; &#X23;",
		},
		{
			name: "escapes unknown and invalid entities",
			in:   "A &copy; B &#xZZ; C &#12x; D &",
			want: "A &amp;copy; B &amp;#xZZ; C &amp;#12x; D &amp;",
		},
		{
			name: "escapes unsupported tags as visible text",
			in:   "<div>x</div><script>alert(1)</script>",
			want: "&lt;div&gt;x&lt;/div&gt;&lt;script&gt;alert(1)&lt;/script&gt;",
		},
		{
			name: "drops unsupported attributes from supported tags",
			in:   `<b onclick="bad">ok</b><code class="language-go" data-x="1">fmt.Println()</code>`,
			want: `<b>ok</b><code>fmt.Println()</code>`,
		},
		{
			name: "preserves supported link attribute escaped",
			in:   `<a href="https://example.com?a=1&b=<x>" onclick="bad">link</a>`,
			want: `<a href="https://example.com?a=1&amp;b=&lt;x&gt;">link</a>`,
		},
		{
			name: "supports telegram custom tags",
			in:   `<tg-spoiler>secret</tg-spoiler><span class="tg-spoiler">more</span><tg-emoji emoji-id="5368324170671202286">👍</tg-emoji><tg-time unix="1777044223" format="HH:mm">now</tg-time>`,
			want: `<tg-spoiler>secret</tg-spoiler><span class="tg-spoiler">more</span><tg-emoji emoji-id="5368324170671202286">👍</tg-emoji><tg-time unix="1777044223" format="HH:mm">now</tg-time>`,
		},
		{
			name: "escapes unsupported span and required-attribute tags",
			in:   `<span class="bad">x</span><a>link</a><tg-emoji>👍</tg-emoji><tg-time>now</tg-time><tg-time datetime="2026-04-25T00:00:00Z">bad</tg-time>`,
			want: `&lt;span class=&#34;bad&#34;&gt;x&lt;/span&gt;&lt;a&gt;link&lt;/a&gt;&lt;tg-emoji&gt;👍&lt;/tg-emoji&gt;&lt;tg-time&gt;now&lt;/tg-time&gt;&lt;tg-time datetime=&#34;2026-04-25T00:00:00Z&#34;&gt;bad&lt;/tg-time&gt;`,
		},
		{
			name: "escapes mismatched closing tags",
			in:   "<b><i>x</b></i>",
			want: "<b><i>x&lt;/b&gt;</i>",
		},
		{
			name: "preserves expandable blockquotes",
			in:   `<blockquote expandable>quote</blockquote><blockquote>plain</blockquote>`,
			want: `<blockquote expandable>quote</blockquote><blockquote>plain</blockquote>`,
		},
		{
			name: "handles self closing supported tags",
			in:   `<blockquote expandable/>`,
			want: `<blockquote expandable/>`,
		},
		{
			name: "escapes incomplete tag opener only",
			in:   "x <",
			want: "x &lt;",
		},
		{
			name: "escapes empty and malformed tags",
			in:   "<> </>",
			want: "&lt;&gt; &lt;/&gt;",
		},
		{
			name: "parses unquoted and single quoted attributes",
			in:   `<a href=https://example.com?a=1&b=2 target=_blank>u</a><code class='language-sh'>echo</code>`,
			want: `<a href="https://example.com?a=1&amp;b=2">u</a><code>echo</code>`,
		},
		{
			name: "preserves language class only for code nested in pre",
			in:   `<pre><code class="language-python">print("x")</code></pre><code class="language-go">fmt.Println()</code>`,
			want: `<pre><code class="language-python">print(&#34;x&#34;)</code></pre><code>fmt.Println()</code>`,
		},
		{
			name: "drops invalid code class",
			in:   `<code class="bad">x</code>`,
			want: `<code>x</code>`,
		},
		{
			name: "escapes plausible unclosed tag opener only",
			in:   "x <b",
			want: "x &lt;b",
		},
		{
			name: "escapes too short numeric hex entity",
			in:   "bad &#x;",
			want: "bad &amp;#x;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := HTML(tt.in); got != tt.want {
				t.Fatalf("HTML() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTMLHelpers(t *testing.T) {
	t.Parallel()

	if tag, _, _, ok := telegramHTMLTag("<>", ""); ok || tag != "" {
		t.Fatalf("telegramHTMLTag(<>) = %q, ok %v; want empty false", tag, ok)
	}
	if got := parseHTMLAttrs("   "); len(got) != 0 {
		t.Fatalf("parseHTMLAttrs(spaces) = %v, want empty", got)
	}
	if got := parseHTMLAttrs("=bad"); len(got) != 0 {
		t.Fatalf("parseHTMLAttrs(=bad) = %v, want empty", got)
	}
	if value, rest := splitHTMLAttrValue(""); value != "" || rest != "" {
		t.Fatalf("splitHTMLAttrValue(empty) = (%q, %q), want empty", value, rest)
	}
	if value, rest := splitHTMLAttrValue(`"unterminated`); value != "unterminated" || rest != "" {
		t.Fatalf("splitHTMLAttrValue(unterminated) = (%q, %q), want unterminated empty", value, rest)
	}
}

func TestHTMLPlainText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "nested supported markup", in: `<p><b>Build</b> <i>passed</i></p>`, want: "Build passed"},
		{name: "entities", in: `A &lt; B &amp;&amp; C &#35;1`, want: "A < B && C #1"},
		{name: "unsafe markup", in: `<script>alert(1)</script><b>safe</b>`, want: "alert(1)safe"},
		{name: "encoded unsafe markup", in: `&lt;script&gt;alert(1)&lt;/script&gt;`, want: "alert(1)"},
		{name: "malformed opener", in: `safe <script`, want: "safe"},
		{name: "ordinary comparison", in: `p95 < 250ms`, want: "p95 < 250ms"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := HTMLPlainText(tt.in); got != tt.want {
				t.Fatalf("HTMLPlainText() = %q, want %q", got, tt.want)
			}
		})
	}
}
