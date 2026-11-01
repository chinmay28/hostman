import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { OllamaHost } from '../types'

// The screen is rendered against a pretend HostMan: whatever `status` holds
// is what the host says about ollama.
let status: OllamaHost
const calls: string[] = []

vi.mock('../api', () => ({
  ApiError: class extends Error {
    status = 0
  },
  api: {
    host: async () => ({ id: 7, name: 'nakedpi', username: 'pi', ollama: true }),
    ollama: async () => status,
    pullOllama: async (_id: number, model: string) => {
      calls.push(`pull ${model}`)
      return status
    },
    ollamaAction: async (_id: number, action: string, model: string) => {
      calls.push(`${action} ${model}`.trim())
      return status
    },
  },
}))

import HostOllama from './HostOllama'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const running: OllamaHost = {
  installed: true,
  version: '0.6.2',
  user: 'pi',
  running: true,
  unit: 'active',
  models: [
    { name: 'llama3.2:latest', id: 'a80c4f17acd5', size: '2.0 GB', sizeBytes: 2_000_000_000, modified: '2 weeks ago' },
    { name: 'qwen2.5-coder:7b', id: '2b0496514337', size: '4.7 GB', sizeBytes: 4_700_000_000, modified: '3 days ago' },
  ],
  loaded: [
    { name: 'llama3.2:latest', id: 'a80c4f17acd5', size: '4.0 GB', processor: '100% GPU', until: '4 minutes from now' },
  ],
}

describe('the ollama screen', () => {
  let host: HTMLDivElement
  let root: Root

  const mount = async () => {
    host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={['/hosts/7/ollama']}>
          <Routes>
            <Route path="/hosts/:id/ollama" element={<HostOllama />} />
            <Route path="/hosts/:id/shell" element={<div id="shell" />} />
          </Routes>
        </MemoryRouter>,
      )
    })
    // Let the loaders answer.
    await act(async () => {
      await Promise.resolve()
    })
  }

  beforeEach(() => {
    calls.length = 0
  })

  afterEach(() => {
    act(() => root.unmount())
    host.remove()
  })

  const text = () => host.textContent ?? ''
  const button = (label: string) =>
    Array.from(host.querySelectorAll('button')).find((b) => b.textContent?.trim() === label)

  it('says what to run where ollama is not installed, and nothing else', async () => {
    status = { installed: false, user: 'pi', running: false, models: [], loaded: [] }
    await mount()
    expect(text()).toContain('Ollama is not installed on nakedpi')
    expect(text()).toContain('curl -fsSL https://ollama.com/install.sh | sh')
    expect(text()).not.toContain('Pull a model')
  })

  it('offers to start a server that is down, and says how', async () => {
    status = {
      installed: true,
      version: '0.6.2',
      user: 'pi',
      running: false,
      unit: 'inactive',
      error: "could not connect to ollama server, run 'ollama serve' to start it",
      models: [],
      loaded: [],
    }
    await mount()
    expect(text()).toContain('The server is not running')
    expect(text()).toContain('could not connect to ollama server')
    expect(text()).toContain('ollama.service is inactive')
    await act(async () => button('Start the server')!.click())
    expect(calls).toEqual(['start'])
  })

  it('lists what is on the disk and in memory, and leads to a conversation', async () => {
    status = running
    await mount()
    expect(text()).toContain('In memory')
    expect(text()).toContain('4.0 GB · 100% GPU · until 4 minutes from now')
    expect(text()).toContain('On the disk')
    expect(text()).toContain('a80c4f17acd5 · 2.0 GB · 2 weeks ago')
    expect(text()).toContain('2 models, 6.2 GB on nakedpi')

    // Talking to a model is the terminal with the command in the query.
    const talk = Array.from(host.querySelectorAll('button')).filter((b) => b.textContent === 'Talk to it')
    expect(talk).toHaveLength(2)
    await act(async () => talk[0].click())
    expect(host.querySelector('#shell')).not.toBeNull()
  })

  it('pulls what was typed, and confirms before removing', async () => {
    status = running
    await mount()
    const input = host.querySelector<HTMLInputElement>('input')!
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    await act(async () => {
      setter.call(input, 'gemma3:4b')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => button('Pull')!.click())
    expect(calls).toEqual(['pull gemma3:4b'])

    await act(async () => Array.from(host.querySelectorAll('button')).find((b) => b.textContent === 'Remove')!.click())
    expect(document.body.textContent).toContain('Remove llama3.2:latest?')
    await act(async () => button('Remove it')!.click())
    expect(calls).toEqual(['pull gemma3:4b', 'remove llama3.2:latest'])
  })

  it('follows a pull with a bar and offers to cancel it', async () => {
    status = {
      ...running,
      pull: { model: 'gemma3:4b', state: 'running', percent: 42, status: 'pulling 8934d96d3f08... 42% 1.7 GB/4.1 GB', log: 'pulling manifest\npulling 8934d96d3f08... 42% 1.7 GB/4.1 GB' },
    }
    await mount()
    expect(text()).toContain('gemma3:4b')
    expect(text()).toContain('42%')
    expect(host.querySelector('[role="meter"]')?.getAttribute('aria-valuenow')).toBe('42')
    expect(host.querySelector('input')).toBeNull()
    await act(async () => button('Cancel')!.click())
    expect(calls).toEqual(['cancel'])
  })

  it('reports how the last pull ended until it is dismissed', async () => {
    status = {
      ...running,
      pull: { model: 'nosuch:model', state: 'failed', exit: 1, percent: -1, status: 'Error: pull model manifest: file does not exist' },
    }
    await mount()
    expect(text()).toContain('failed (exit 1)')
    expect(text()).toContain('file does not exist')
    expect(host.querySelector('input')).not.toBeNull()
    await act(async () => button('Dismiss')!.click())
    expect(calls).toEqual(['clear'])
  })
})
