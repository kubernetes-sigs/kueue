# Support internationalization for Kueue website

This guide explains how to add internationalization support for a new language to the Kueue website.

Sample: Add Simplified Chinese (zh-CN) support.

### Hugo Configuration

Add your new language configuration to `site/hugo.toml`:

```toml
[languages]
  [languages.en]
    ...
    
  [languages.zh-CN]
    ...

  # Add your new language here
  [languages.LANG_CODE]
    title = "Kueue"
    languageName = "Your Language Name"
    weight = 3
    contentDir = "content/LANG_CODE"
    [languages.LANG_CODE.params]
    language_code = "LANG_CODE"
```

**Key points:**
- Replace `LANG_CODE` with the appropriate language code (e.g., `fr`, `de`, `ja`)
- Use standard language codes from [RFC 5646](https://tools.ietf.org/html/rfc5646)
- Set `weight` to control the order in language switcher
- `languageName` appears in the language dropdown menu

### Create i18n File

Create a new translation file at `site/i18n/LANG_CODE.yaml`:

**Translation guidelines:**
- Keep technical terms like "Kueue" untranslated
- Maintain consistent terminology throughout
- Consider cultural context for UI elements

### Translate Main Pages

#### Homepage (`site/content/LANG_CODE/_index.html`)

```html
+++
title = "Kueue"
linkTitle = "Kueue"
description = "Translated description of Kueue"
+++

{{< blocks/cover title="Welcome to Kueue" image_anchor="top" height="full" color="orange" >}}
<div class="mx-auto">
	<a class="btn btn-lg btn-primary mr-3 mb-4" href="/LANG_CODE/docs/">
		Read Documentation <i class="fas fa-arrow-alt-circle-right ml-2"></i>
	</a>
	<a class="btn btn-lg btn-secondary mr-3 mb-4" href="https://github.com/kubernetes-sigs/kueue">
		GitHub <i class="fab fa-github ml-2 "></i>
	</a>
	<p class="lead mt-5">Kubernetes-native job queueing</p>
	{{< blocks/link-down color="info" id="description" >}}
</div>
{{< /blocks/cover >}}
```

### Sync Documentation

Copy the English version of the documentation pages to the new language directory.

```bash
cp -r site/content/en/* site/content/LANG_CODE/
```

### Test

Start the development server:
```bash
make site-server
```

Verify the new language:
- Visit `http://localhost:1313/LANG_CODE/`
- Test navigation between pages
- Verify all UI elements are translated
