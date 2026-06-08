<script>
  import { logEntries } from '../lib/stores.js';

  let collapsed = false;
  let logBody;

  $: if (logBody && $logEntries) {
    // auto-scroll on new entries
    setTimeout(() => { logBody.scrollTop = logBody.scrollHeight; }, 0);
  }

  function toggle() {
    collapsed = !collapsed;
  }

  function levelClass(level) {
    if (level === 'error') return 'text-error';
    if (level === 'warn') return 'text-warning';
    if (level === 'success') return 'text-success';
    return 'text-base-content/70';
  }
</script>

<div class="bg-base-200 border-t border-base-content/10 shrink-0 max-h-[250px] flex flex-col" class:max-h-8={collapsed}>
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="flex justify-between items-center px-4 py-2 cursor-pointer border-b border-base-content/10" on:click={toggle}>
    <span class="text-sm font-semibold">实时日志</span>
    <span class="text-xs text-base-content/50">{collapsed ? '展开' : '收起'}</span>
  </div>
  {#if !collapsed}
    <div bind:this={logBody} class="flex-1 overflow-y-auto px-4 py-2 font-mono text-xs leading-relaxed">
      {#each $logEntries as entry}
        <div class="flex gap-2">
          <span class="text-base-content/40 shrink-0">{entry.time}</span>
          <span class={levelClass(entry.level)}>{entry.msg}</span>
        </div>
      {/each}
    </div>
  {/if}
</div>
