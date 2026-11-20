# Interface localization

Turkish remains the default. There is no public locale selector or browser
language negotiation yet. For an isolated test instance only, add
`preview_locale: en` to its YAML configuration. This is an instance-wide,
incomplete preview, not a production setting. Remove the key to return to
Turkish. Unsupported values fail configuration loading.

The shared layout, home widgets, latest activity, search, tag pages,
notifications, 404 page, authentication forms, forum listings, member profiles
and external media labels use message keys in
`internal/web/locale.go`. The catalog handles the plural rules of its two
current languages and formats relative dates against the request timestamp.
User posts, forum titles, sponsor text and plugin notices are not translated.
Authentication validation errors and profile-save errors are also cataloged;
unexpected account-creation failures are logged server-side rather than exposed
in the UI. Thread pages, thread creation, reply/edit/report controls, reactions
and poll controls also use the catalogs. Poll questions and choices remain
user content. Many server-side thread-action error messages still contain
Turkish copy. The administration dashboard, report queue and
permission matrix now use catalogs, including built-in group names and
permission labels. Stored permission keys and form values are unchanged.
The preview does not establish RTL support.

Forum structure management is also cataloged, including its validation errors
and deletion confirmation. The current parent category is selected separately
for each forum edit form.

Appearance management, draft preview, settings rollback, installed WASM status
and their validation errors use `internal/web/locale_appearance.go`. Tests run
theme preview/save/restore and dependency enable/disable flows in both current
languages. Literal server-side message lookups and template keys are checked
against both catalogs; this detects missing keys, not untranslated hardcoded
copy, translation quality or complete language support.

When moving copy into the catalog, use complete phrases rather than joining
translated fragments. Use positional placeholders for phrases where arguments
may change order. Keep messages plain text so `html/template` escapes content
in the context where it is rendered. Add coverage for the new keys and screen.

A production catalog needs coverage across all templates and server responses,
locale-appropriate plural forms and date/number formats, and authenticated and
guest acceptance tests. For RTL languages the layout must also be checked in a
browser, not just for the presence of `dir="rtl"`.
