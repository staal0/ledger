// Small progressive enhancements. Everything also works without JavaScript.
document.addEventListener('DOMContentLoaded', () => {
  for (const b of document.querySelectorAll('button[data-confirm]')) {
    b.addEventListener('click', e => { if (!confirm(b.dataset.confirm)) e.preventDefault() })
  }

  for (const s of document.querySelectorAll('select[data-autosubmit]')) {
    s.addEventListener('change', () => s.form.submit())
  }

  // Theme toggle: switch at once and save in the background, so unsaved edits survive.
  const toggle = document.querySelector('button.theme')
  if (toggle) toggle.addEventListener('click', e => {
    e.preventDefault()
    const root = document.documentElement
    const dark = root.dataset.theme === 'dark' ||
      (!root.dataset.theme && matchMedia('(prefers-color-scheme: dark)').matches)
    const theme = dark ? 'light' : 'dark'
    root.dataset.theme = theme
    fetch('/theme', { method: 'POST', body: new URLSearchParams({ theme }), redirect: 'manual' })
  })

  // Money in the user's format. Amounts are in øre.
  const dec = document.body.dataset.decimal, grp = document.body.dataset.thousands
  const plain = s => grp ? s.split(grp).join('') : s
  const parse = s => Math.round(parseFloat(plain(s).replace(dec, '.')) * 100) || 0
  const fmt = c => {
    const neg = c < 0
    c = Math.abs(c)
    let s = String(Math.floor(c / 100)).replace(/\B(?=(\d{3})+(?!\d))/g, grp)
    if (c % 100) s += dec + String(c % 100).padStart(2, '0')
    return (neg ? '-' : '') + s
  }
  const kr = c => fmt(Math.round(c / 100) * 100) // whole kroner

  // Chart helpers. Built with textContent only: line names are user data.
  const el = (tag, cls, text) => {
    const e = document.createElement(tag)
    if (cls) e.className = cls
    if (text != null) e.textContent = text
    return e
  }

  const tip = el('div', 'tooltip')
  tip.hidden = true
  document.body.append(tip)
  const hideTip = () => { tip.hidden = true }
  function hover(target, title, rows) {
    target.tabIndex = 0
    const show = () => {
      tip.replaceChildren(el('div', 'tip-title', title))
      for (const [cls, value, label] of rows) {
        const row = el('div', 'tip-row')
        row.append(el('i', 'key ' + cls), el('strong', null, value), el('span', null, label))
        tip.append(row)
      }
      tip.hidden = false
      const r = target.getBoundingClientRect()
      const maxLeft = document.documentElement.clientWidth - tip.offsetWidth - 8
      tip.style.left = scrollX + Math.max(8, Math.min(r.left, maxLeft)) + 'px'
      tip.style.top = scrollY + r.top - tip.offsetHeight - 6 + 'px'
    }
    target.addEventListener('pointerenter', show)
    target.addEventListener('focus', show)
    target.addEventListener('pointerleave', hideTip)
    target.addEventListener('blur', hideTip)
  }

  // Axis ticks: a 1/2/2.5/5 step giving about four gridlines, labelled compactly (20k).
  const niceStep = x => {
    const p = 10 ** Math.floor(Math.log10(x))
    return [1, 2, 2.5, 5, 10].find(f => f * p >= x) * p
  }
  const compact = c => {
    const v = c / 100
    return v >= 1000 ? String(+(v / 1000).toFixed(1)).replace('.', dec) + 'k' : String(v)
  }

  // Income vs expenses as paired columns, one group per label. The tooltip
  // also shows the net. short holds the axis labels for a narrow chart.
  function columns(box, labels, short, income, expense) {
    const L = box.dataset
    const top0 = Math.max(...income, ...expense, 100)
    const step = niceStep(top0 / 4)
    const top = Math.ceil(top0 / step) * step
    const plot = el('div', 'plot')
    for (let v = 0; v <= top; v += step) {
      const g = el('div', 'grid')
      g.style.bottom = v / top * 100 + '%'
      g.append(el('span', null, compact(v)))
      plot.append(g)
    }
    labels.forEach((name, i) => {
      const m = el('div', 'month')
      for (const [cls, v] of [['income', income[i]], ['expense', expense[i]]]) {
        const b = el('i', 'bar ' + cls)
        b.style.height = Math.max(0, v) / top * 100 + '%'
        m.append(b)
      }
      hover(m, name, [['income', kr(income[i]), L.income], ['expense', kr(expense[i]), L.expense], ['', kr(income[i] - expense[i]), L.net]])
      plot.append(m)
    })
    const axis = el('div', 'xaxis')
    labels.forEach((name, i) => {
      const s = el('span')
      s.append(el('b', 'long', name), el('b', 'short', short[i]))
      axis.append(s)
    })
    box.replaceChildren(plot, axis)
  }

  // Statistics page: income and expenses per year.
  const yearsChart = document.querySelector('[data-chart="years"]')
  if (yearsChart) {
    const d = JSON.parse(document.getElementById('stats-data').textContent)
    const labels = d.Years.map(String)
    columns(yearsChart, labels, labels.map(y => "'" + y.slice(2)), d.Income, d.Expense)
  }

  const form = document.querySelector('form.budget')
  if (!form) return

  // Keyboard help: the toolbar button, or "?" outside text fields.
  const help = document.querySelector('dialog.help')
  const showHelp = () => help.showModal()
  document.querySelector('button[data-help]').addEventListener('click', showHelp)
  document.addEventListener('keydown', e => {
    if (e.key === '?' && !e.target.closest('input, select, textarea') && !help.open) showHelp()
  })

  // Spreadsheet-like keys in the grid. In amount fields the arrows and h/j/k/l
  // always move; in name fields left/right only leave at the edge of the text.
  // Enter saves.
  // More vim keys in amount fields: ^/$ first/last month, gg/G first/last line, { } previous/next
  // section, i/a cursor at start/end, Esc select again, x clear, u restore saved value, y copy, p paste,
  // f copy to the following months.
  const rows = [...form.querySelectorAll('tr.line')]
  const sections = [...form.querySelectorAll('tbody[data-section]')].filter(b => b.querySelector('tr.line'))
  const go = inp => { if (inp) { inp.focus(); inp.select() } }
  const keys = { ArrowUp: 'up', ArrowDown: 'down', ArrowLeft: 'left', ArrowRight: 'right' }
  const vim = { h: 'left', j: 'down', k: 'up', l: 'right' }
  let pendingG = false, yanked = null
  for (const inp of form.querySelectorAll('input.num')) inp.dataset.saved = inp.value // for "u"
  form.addEventListener('keydown', e => {
    const inp = e.target
    const tr = inp.tagName === 'INPUT' && inp.closest('tr')
    if (!tr || e.altKey || e.ctrlKey || e.metaKey) return
    if (e.key === 'Enter') {
      e.preventDefault()
      form.requestSubmit(form.querySelector('button[value="save"]'))
      return
    }
    const num = inp.classList.contains('num')
    const cells = [...tr.querySelectorAll('input')]
    const col = cells.indexOf(inp)
    const cellIn = row => row && row.querySelectorAll('input')[col]
    const set = v => { inp.value = v; inp.dispatchEvent(new Event('input', { bubbles: true })) }
    const sectionStart = step => {
      const s = sections[sections.indexOf(tr.closest('tbody')) + step]
      return s && s.querySelector('tr.line')
    }
    const g = pendingG
    pendingG = false
    const edit = num && {
      g: () => { if (g) go(cellIn(rows[0])); else pendingG = true },
      G: () => go(cellIn(rows.at(-1))),
      '^': () => go(cells[1]), // January; cells[0] is the name
      $: () => go(cells.at(-1)), // December
      Home: () => go(cells[1]),
      End: () => go(cells.at(-1)),
      '{': () => go(cellIn(sectionStart(-1))),
      '}': () => go(cellIn(sectionStart(1))),
      '?': showHelp,
      i: () => inp.setSelectionRange(0, 0), // drop the highlight, cursor at the start
      a: () => inp.setSelectionRange(inp.value.length, inp.value.length), // … at the end
      Escape: () => inp.select(), // highlight the value again
      x: () => set(''),
      f: () => fill(inp),
      u: () => set(inp.dataset.saved),
      y: () => { yanked = inp.value },
      p: () => { if (yanked !== null) set(yanked) },
    }[e.key]
    if (edit) {
      e.preventDefault()
      return edit()
    }
    const dir = keys[e.key] || (num && vim[e.key])
    if (!dir) {
      // Amount fields take only digits, minus and the decimal separator; other
      // printable keys are ignored. Editing keys (Backspace, Tab, ...) pass through.
      if (num && e.key.length === 1 && !/[0-9-]/.test(e.key) && e.key !== dec) e.preventDefault()
      return
    }
    if (dir === 'up' || dir === 'down') {
      e.preventDefault()
      const step = dir === 'up' ? -1 : 1
      for (let i = rows.indexOf(tr) + step; i >= 0 && i < rows.length; i += step) {
        const target = rows[i].querySelectorAll('input')[col]
        if (target) return go(target)
      }
      return
    }
    const edge = dir === 'left'
      ? inp.selectionStart === 0 && inp.selectionEnd === 0
      : inp.selectionStart === inp.value.length
    if (!num && !edge) return
    e.preventDefault()
    go(cells[col + (dir === 'left' ? -1 : 1)])
  })

  // Fill ("f"): copy an amount to the following months of its line.
  function fill(from) {
    const cells = [...from.closest('tr').querySelectorAll('input.num')]
    const v = parse(from.value)
    for (const c of cells.slice(cells.indexOf(from) + 1)) c.value = v ? fmt(v) : ''
    from.dispatchEvent(new Event('input', { bubbles: true }))
  }

  // Unsaved changes: a dot on Save, and the browser asks before leaving the page.
  // Amounts compare as numbers, so reformatting a field is not a change.
  const save = form.querySelector('button[value="save"]')
  const changed = inp => inp.classList.contains('num')
    ? parse(inp.value) !== parse(inp.defaultValue)
    : inp.value.trim() !== inp.defaultValue
  let submitting = false
  form.addEventListener('input', () => {
    const dirty = [...form.querySelectorAll('tr.line input')].some(changed)
    save.classList.toggle('dirty', dirty)
    save.title = dirty ? save.dataset.unsaved : ''
  })
  form.addEventListener('submit', () => { submitting = true })
  addEventListener('pageshow', () => { submitting = false })
  addEventListener('beforeunload', e => {
    if (!submitting && save.classList.contains('dirty')) e.preventDefault()
  })

  // After a save the page reloads: come back to the same field and scroll position.
  const restoreKey = 'ledger-restore'
  form.addEventListener('submit', () => {
    const a = document.activeElement
    try {
      sessionStorage.setItem(restoreKey, JSON.stringify({
        path: location.pathname,
        field: a && a.tagName === 'INPUT' ? a.name : '',
        y: scrollY,
        x: form.querySelector('.scroll').scrollLeft,
      }))
    } catch {}
  })
  try {
    const r = JSON.parse(sessionStorage.getItem(restoreKey) || 'null')
    sessionStorage.removeItem(restoreKey)
    if (r && r.path === location.pathname) {
      form.querySelector('.scroll').scrollLeft = r.x
      scrollTo(0, r.y)
      const f = r.field && form.querySelector(`input[name="${CSS.escape(r.field)}"]`)
      if (f) { f.focus({ preventScroll: true }); f.select() }
    }
  } catch {}

  // A line was just added: select its placeholder name, so typing replaces it.
  // The page stays where it was and only scrolls if the new line is out of view.
  const addedID = new URLSearchParams(location.search).get('added')
  if (addedID) {
    history.replaceState(null, '', location.pathname)
    const added = form.querySelector(`#line-${CSS.escape(addedID)} input`)
    if (added) {
      added.focus({ preventScroll: true })
      added.select()
      added.scrollIntoView({ block: 'nearest' })
    }
  }

  // Amounts show thousands separators, except while a field is being edited.
  form.addEventListener('focusin', e => {
    if (e.target.classList.contains('num')) e.target.value = plain(e.target.value)
  })
  form.addEventListener('focusout', e => {
    const inp = e.target
    if (!inp.classList.contains('num') || !inp.checkValidity()) return
    const v = parse(inp.value)
    inp.value = v ? fmt(v) : ''
  })

  // Live totals in the budget grid, mirroring the server's calculation.
  function recalc() {
    const t = {}, lines = {}
    for (const tb of form.querySelectorAll('tbody[data-section]')) {
      const sum = Array(13).fill(0)
      lines[tb.dataset.section] = []
      for (const tr of tb.querySelectorAll('tr.line')) {
        let total = 0
        tr.querySelectorAll('input.num').forEach((inp, i) => {
          const v = parse(inp.value)
          sum[i + 1] += v
          total += v
        })
        sum[0] += total
        tr.querySelector('.total').textContent = fmt(total)
        lines[tb.dataset.section].push({ name: tr.querySelector('input').value, total })
      }
      t[tb.dataset.section] = sum
    }
    const net = m => t.income[m] - t.expense[m]
    const calc = {
      income: m => t.income[m],
      expense: m => t.expense[m],
      allocation: m => t.allocation[m],
      net,
      difference: m => net(m) - t.allocation[m],
    }
    for (const tr of form.querySelectorAll('tr[data-calc]')) {
      const f = calc[tr.dataset.calc]
      tr.querySelectorAll('td.num').forEach((td, i) => {
        const v = f(i < 12 ? i + 1 : 0)
        td.textContent = fmt(v)
        td.classList.toggle('neg', v < 0)
        td.classList.toggle('pos', v > 0)
      })
    }
    if (charts) drawCharts(t, lines, calc)
  }

  // Charts next to the grid.
  const charts = document.querySelector('.charts')
  const L = charts ? charts.dataset : {}
  const months = [...form.querySelectorAll('tr.head')[0].querySelectorAll('th')].slice(1, 13).map(th => th.textContent)

  // Largest lines first; past eight, the tail folds into "Other".
  function hbars(box, lines) {
    let rows = lines.filter(l => l.total > 0).sort((a, b) => b.total - a.total)
    const all = rows.reduce((s, l) => s + l.total, 0)
    if (rows.length > 8) {
      const rest = rows.slice(7).reduce((s, l) => s + l.total, 0)
      rows = rows.slice(0, 7).concat({ name: L.other, total: rest, other: true })
    }
    box.replaceChildren()
    if (!rows.length) box.append(el('p', 'hint', L.empty))
    const max = Math.max(1, ...rows.map(r => r.total)) // "Other" can outgrow the largest line
    for (const r of rows) {
      const cls = r.other ? 'other' : box.dataset.chart
      const row = el('div', 'hrow')
      const track = el('span', 'track')
      const bar = el('i', 'bar ' + cls)
      bar.style.width = r.total / max * 100 + '%'
      track.append(bar)
      row.append(el('span', 'name', r.name), track, el('span', 'val', kr(r.total)))
      hover(row, r.name, [[cls, kr(r.total), Math.round(r.total / all * 100) + ' %']])
      box.append(row)
    }
  }

  function drawCharts(t, lines, calc) {
    for (const tile of charts.querySelectorAll('[data-tile]')) {
      const v = calc[tile.dataset.tile](0)
      tile.querySelector('.value').textContent = kr(v)
      if (tile.dataset.tile === 'difference') tile.dataset.sign = v > 0 ? 'pos' : v < 0 ? 'neg' : ''
    }
    const monthly = document.querySelector('[data-chart="monthly"]')
    columns(monthly, months, months.map(m => m[0]), t.income.slice(1), t.expense.slice(1))
    for (const box of document.querySelectorAll('.hbars')) hbars(box, lines[box.dataset.chart])
  }

  form.addEventListener('input', recalc)
  recalc()
})
