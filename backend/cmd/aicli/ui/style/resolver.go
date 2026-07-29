package style

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// Resolver maps semantic roles onto concrete styles under a ThemeContext.
type Resolver struct {
	Ctx ThemeContext
}

// NewResolver builds a resolver from an already-resolved theme context.
func NewResolver(ctx ThemeContext) Resolver {
	return Resolver{Ctx: ctx}
}

// Resolve returns the effective style for a role under the current profile.
func (r Resolver) Resolve(role Role) render.Style {
	style := r.Ctx.Palette.StyleFor(role)
	return AdaptStyle(style, r.Ctx.Terminal)
}

// ResolveSpan applies palette role (if present) then profile adaptation.
func (r Resolver) ResolveSpan(span render.Span) render.Span {
	out := span
	if span.Style.Role != "" {
		roleStyle := r.Resolve(Role(span.Style.Role))
		out.Style = render.MergeStyles(roleStyle, span.Style)
		// Keep role label for golden tests.
		out.Style.Role = span.Style.Role
	} else {
		out.Style = AdaptStyle(span.Style, r.Ctx.Terminal)
	}
	return out
}

// ResolveDocument walks all spans and materializes role styles.
func (r Resolver) ResolveDocument(doc render.Document) render.Document {
	out := render.Document{Blocks: make([]render.Block, len(doc.Blocks))}
	for i, block := range doc.Blocks {
		nb := block
		nb.Lines = make([]render.Line, len(block.Lines))
		for j, line := range block.Lines {
			nl := line
			if line.Style.Role != "" {
				nl.Style = r.Resolve(Role(line.Style.Role))
				nl.Style.Role = line.Style.Role
			} else {
				nl.Style = AdaptStyle(line.Style, r.Ctx.Terminal)
			}
			nl.Spans = make([]render.Span, len(line.Spans))
			for k, span := range line.Spans {
				nl.Spans[k] = r.ResolveSpan(span)
			}
			nb.Lines[j] = nl
		}
		out.Blocks[i] = nb
	}
	return out
}

// AdaptStyle downgrades colors for the active color depth.
func AdaptStyle(style render.Style, profile ColorProfile) render.Style {
	if !profile.Enabled || profile.Depth == render.ColorNone {
		// Preserve modifiers that remain useful without color (bold/dim/reverse).
		return render.Style{
			Bold:      style.Bold,
			Dim:       style.Dim,
			Italic:    style.Italic,
			Underline: style.Underline,
			Reverse:   style.Reverse,
			Role:      style.Role,
		}
	}
	out := style
	out.Foreground = adaptColor(style.Foreground, profile.Depth)
	out.Background = adaptColor(style.Background, profile.Depth)
	if profile.Depth == render.ColorANSI16 {
		// Drop custom backgrounds on 16-color terminals.
		out.Background = render.DefaultColor()
	}
	return out
}

func adaptColor(c render.Color, depth render.ColorDepth) render.Color {
	if !c.IsSet() {
		return c
	}
	switch depth {
	case render.ColorNone:
		return render.DefaultColor()
	case render.ColorANSI16:
		if c.Kind == render.ColorANSI {
			return c
		}
		// Quantization happens in the ANSI backend; keep RGB for distance map.
		return c
	case render.ColorANSI256:
		if c.Kind == render.ColorRGB {
			// Leave RGB; backend quantizes. Could pre-quantize here later.
			return c
		}
		return c
	default:
		return c
	}
}

// BuildThemeContext assembles a ThemeContext from selection + profile.
func BuildThemeContext(sel ThemeSelection, profile ColorProfile) ThemeContext {
	variant := VariantDark
	switch sel.Mode {
	case ThemeModeLight:
		variant = VariantLight
	case ThemeModeDark:
		variant = VariantDark
	default:
		switch profile.Background {
		case BackgroundLight:
			variant = VariantLight
		default:
			variant = VariantDark
		}
	}
	palette := NewPalette(sel.PaletteName, variant)
	syntax := strings.TrimSpace(sel.SyntaxName)
	if syntax == "" || strings.EqualFold(syntax, "auto") {
		if variant == VariantLight {
			syntax = "github"
		} else {
			syntax = "monokai"
		}
	}
	return ThemeContext{
		Palette:      palette,
		SyntaxName:   syntax,
		Terminal:     profile,
		UseHyperlink: profile.Hyperlinks,
	}
}

// RenderDocument resolves roles then encodes with the matching backend.
func RenderDocument(doc render.Document, ctx ThemeContext) string {
	resolved := NewResolver(ctx).ResolveDocument(doc)
	if !ctx.Terminal.Enabled || ctx.Terminal.Depth == render.ColorNone {
		return render.PlainBackend{}.Render(resolved)
	}
	backend := render.ANSIBackend{
		Profile: render.ColorProfile{
			Enabled:    ctx.Terminal.Enabled,
			Depth:      ctx.Terminal.Depth,
			Hyperlinks: ctx.UseHyperlink && ctx.Terminal.Hyperlinks,
			Forced:     ctx.Terminal.Forced,
		},
	}
	return backend.Render(resolved)
}
