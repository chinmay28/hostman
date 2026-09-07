import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppHeader } from './Layout'

// React's act() wants to be told it is running under a test harness.
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('the brand mark', () => {
  let host: HTMLDivElement
  let root: Root

  beforeEach(() => {
    host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    act(() => {
      root.render(
        <MemoryRouter>
          <AppHeader />
        </MemoryRouter>,
      )
    })
  })

  afterEach(() => {
    act(() => root.unmount())
    host.remove()
  })

  const mark = () => host.querySelector<HTMLButtonElement>('.brand-mark')!
  const logo = () => document.body.querySelector('.brand-flash-logo')

  it('shows the logo over the app on a double tap', () => {
    act(() => mark().click())
    expect(logo()).toBeNull()
    act(() => mark().click())
    expect(logo()).not.toBeNull()
    expect(document.body.textContent).toContain('All your hosts in good hands')
  })

  it('clears the logo when the overlay is tapped', () => {
    act(() => {
      mark().click()
      mark().click()
    })
    expect(logo()).not.toBeNull()
    act(() => (document.body.querySelector('.dev-flash') as HTMLElement).click())
    expect(logo()).toBeNull()
  })

  it('does nothing on a single tap', () => {
    act(() => mark().click())
    expect(logo()).toBeNull()
  })
})
