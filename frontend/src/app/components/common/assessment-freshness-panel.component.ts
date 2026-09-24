import { ChangeDetectionStrategy, Component, Input, computed, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { AssessmentFreshness, DoseBudgetAssessment } from '../../types/dose';

@Component({
  selector: 'app-assessment-freshness-panel',
  standalone: true,
  imports: [CommonModule],
  template: `
    <section *ngIf="freshness() as fresh" class="freshness" [class.stale]="!fresh.is_fresh" [class.current]="fresh.is_fresh"
             [attr.aria-label]="fresh.is_fresh ? 'Snapshot freshness confirmed' : 'Snapshot is stale'">
      <header>
        <span class="eyebrow">Snapshot freshness check · {{ fresh.checked_at | date:'medium':'UTC' }}</span>
        <strong>{{ fresh.is_fresh ? 'Snapshot matches current inputs' : 'Snapshot is stale — acceptance blocked' }}</strong>
      </header>
      <div class="net-dose">
        <span>Snapshot period dose <strong>{{ fresh.snapshot_period_dose_msv | number:'1.3-3' }}</strong> mSv</span>
        <span>Current replayed dose <strong>{{ fresh.current_period_dose_msv | number:'1.3-3' }}</strong> mSv</span>
        <span class="delta" [class.negative]="fresh.period_dose_delta_msv < 0" [class.positive]="fresh.period_dose_delta_msv > 0">
          Net period difference <strong>{{ fresh.period_dose_delta_msv > 0 ? '+' : '' }}{{ fresh.period_dose_delta_msv | number:'1.3-3' }}</strong> mSv
        </span>
        <span class="versions" *ngIf="fresh.snapshot_worker_version !== fresh.current_worker_version">
          Worker profile v{{ fresh.snapshot_worker_version }} → v{{ fresh.current_worker_version }}
        </span>
      </div>
      <ul *ngIf="!fresh.is_fresh" class="reasons">
        <li *ngFor="let reason of fresh.reasons">{{ reason }}</li>
      </ul>
      <div *ngIf="fresh.entry_changes.length" class="change-block">
        <h3>Changed exposure records ({{ fresh.entry_changes.length }})</h3>
        <table>
          <thead><tr><th>Record</th><th>Change</th><th>Snapshot</th><th>Now</th><th>Dose mSv</th></tr></thead>
          <tbody>
            <tr *ngFor="let change of fresh.entry_changes">
              <td><code>{{ change.source_ref || '#' + change.entry_id }}</code></td>
              <td>{{ changeLabel(change.change_type) }}</td>
              <td>{{ stateLabel(change.snapshot_state) }}</td>
              <td>{{ stateLabel(change.current_state) }}</td>
              <td [class.negative]="change.dose_msv < 0">{{ change.dose_msv | number:'1.3-3' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div *ngIf="fresh.limit_changes.length" class="change-block">
        <h3>Adjusted worker limits ({{ fresh.limit_changes.length }})</h3>
        <table>
          <thead><tr><th>Limit</th><th>Snapshot mSv</th><th>Current mSv</th></tr></thead>
          <tbody>
            <tr *ngFor="let change of fresh.limit_changes">
              <td>{{ limitLabel(change.limit_name) }}</td>
              <td>{{ change.snapshot_msv | number:'1.3-3' }}</td>
              <td><strong>{{ change.current_msv | number:'1.3-3' }}</strong></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div *ngIf="fresh.threshold_changed" class="change-block threshold">
        Configured threshold version moved from <code>{{ fresh.snapshot_threshold_version }}</code>
        to <code>{{ fresh.current_threshold_version }}</code> after the assessment.
      </div>
      <footer *ngIf="fresh.is_fresh">
        Replayed from current exposure records and worker limits; no post-assessment changes detected.
      </footer>
      <footer *ngIf="!fresh.is_fresh">
        The frozen assessment remains traceable below. Return it for reassessment to evaluate current inputs;
        only a newly generated assessment can be accepted.
      </footer>
    </section>
  `,
  styles: [`
    .freshness { border: 1px solid var(--line); border-radius: 4px; overflow: hidden; margin-top: 10px; font-size: 12px; }
    .freshness.stale { background: #fdf1ef; border-color: #c9877d; }
    .freshness.current { background: #eef5ee; border-color: #9cbc9c; }
    header { padding: 12px 16px; border-bottom: 1px solid var(--line); display: grid; gap: 2px; }
    .stale header { background: #f7e0db; }
    .current header { background: #dceadc; }
    header strong { font-size: 13px; }
    .net-dose { display: flex; flex-wrap: wrap; gap: 18px; padding: 12px 16px; border-bottom: 1px dashed var(--line); }
    .net-dose strong { font-variant-numeric: tabular-nums; }
    .delta.positive { color: #8f2d1c; font-weight: 700; }
    .delta.negative { color: #7a4a08; font-weight: 700; }
    .versions { color: #76510b; font-weight: 700; }
    .reasons { margin: 0; padding: 10px 16px 10px 32px; display: grid; gap: 4px; color: #6b2a1d; }
    .change-block { padding: 10px 16px; border-top: 1px dashed var(--line); }
    .change-block h3 { margin: 0 0 6px; font-size: 11px; text-transform: uppercase; color: var(--muted); }
    table { width: 100%; border-collapse: collapse; font-size: 11px; }
    th { text-align: left; color: var(--muted); font-weight: 500; padding: 3px 8px 3px 0; }
    td { padding: 3px 8px 3px 0; border-top: 1px solid rgba(0,0,0,.06); font-variant-numeric: tabular-nums; }
    td.negative { color: #7a4a08; }
    code { font-size: 10px; }
    .threshold { color: #6b2a1d; }
    footer { padding: 10px 16px; font-size: 11px; color: var(--muted); border-top: 1px solid var(--line); }
    .stale footer { background: #f7e0db; color: #6b2a1d; }
    .current footer { background: #dceadc; color: #27502c; }
  `],
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AssessmentFreshnessPanelComponent {
  private readonly assessmentSignal = signal<DoseBudgetAssessment | null>(null);
  readonly freshness = computed<AssessmentFreshness | null>(() => this.assessmentSignal()?.freshness ?? null);

  @Input({ required: true }) set assessment(value: DoseBudgetAssessment) {
    this.assessmentSignal.set(value);
  }

  changeLabel(change: string): string {
    switch (change) {
      case 'entry_added': return 'New verified record';
      case 'entry_removed': return 'No longer in period window';
      case 'entry_quality_changed': return 'Quality review changed';
      default: return change;
    }
  }

  stateLabel(state: string): string {
    if (!state) return '—';
    if (state === 'included') return 'counted in dose';
    if (state === 'not_present') return 'not present';
    if (state.startsWith('excluded:')) {
      const quality = state.slice('excluded:'.length);
      return `excluded (${quality})`;
    }
    return state;
  }

  limitLabel(name: string): string {
    return name === 'annual_legal_limit_msv' ? 'Annual legal limit' : 'Administrative limit';
  }
}
